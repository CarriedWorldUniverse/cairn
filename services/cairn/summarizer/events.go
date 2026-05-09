// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Package summarizer event-driven auto-run: a Forgejo notifier listens for
// PR open / synchronize, debounces rapid synchronizations, then runs
// Service.EnsureSummary in the background. Errors never propagate — the
// notifier path must not affect the underlying PR action.
package summarizer

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/git"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/services/notify"
)

// Job is one queued summarization request.
type Job struct {
	RepoID   int64
	PRNumber int64
	OwnerID  int64
	Context  PRContext
	Scope    cairnmodels.DataScope
}

// queue debounces summarization runs per (repo_id, pr_number) so that a burst
// of synchronizations on the same PR coalesces into one EnsureSummary call.
type queue struct {
	mu       sync.Mutex
	pending  map[string]*Job
	debounce time.Duration
	svc      *Service
}

func newQueue(svc *Service, debounce time.Duration) *queue {
	return &queue{
		pending:  make(map[string]*Job),
		debounce: debounce,
		svc:      svc,
	}
}

func queueKey(repoID, prNumber int64) string {
	return fmt.Sprintf("%d:%d", repoID, prNumber)
}

// enqueue records the latest Job for (repo, pr) and schedules a delayed
// run after the debounce window. If another enqueue replaces this Job
// before the timer fires, the superseded run is a no-op (the timer
// re-reads the pending map and only acts if the slot still holds it).
func (q *queue) enqueue(j Job) {
	key := queueKey(j.RepoID, j.PRNumber)
	jobCopy := j
	q.mu.Lock()
	q.pending[key] = &jobCopy
	q.mu.Unlock()

	go func() {
		time.Sleep(q.debounce)
		q.mu.Lock()
		current, ok := q.pending[key]
		if !ok || current != &jobCopy {
			q.mu.Unlock()
			return
		}
		delete(q.pending, key)
		svc := q.svc
		q.mu.Unlock()

		if svc == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := svc.EnsureSummary(ctx, jobCopy.RepoID, jobCopy.PRNumber, jobCopy.OwnerID, jobCopy.Context, jobCopy.Scope); err != nil {
			log.Warn("cairn summarizer: auto-run failed for repo=%d pr=%d: %v", jobCopy.RepoID, jobCopy.PRNumber, err)
		}
	}()
}

// maxAutoDiffBytes caps the diff buffered for the auto-run path. Matches the
// cap used by the markdown renderer (routers/web/cairn/forgejo/bind.go).
const maxAutoDiffBytes = 512 * 1024

// limitWriter wraps an io.Writer and stops forwarding once `remaining` bytes
// have been written, marking itself truncated. Mirrors the renderer's pattern.
type limitWriter struct {
	w         strings.Builder
	remaining int
	truncated bool
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.remaining <= 0 {
		lw.truncated = true
		return len(p), nil
	}
	if len(p) > lw.remaining {
		n, err := lw.w.Write(p[:lw.remaining])
		lw.remaining = 0
		lw.truncated = true
		if err != nil {
			return n, err
		}
		return len(p), nil
	}
	n, err := lw.w.Write(p)
	lw.remaining -= n
	return n, err
}

// BuildPRContextFromForgejo loads PR data from Forgejo into a PRContext.
// Best-effort: a failure on any individual data source is logged and the
// available fields are returned. Diff loading is skipped unless scope == full.
func BuildPRContextFromForgejo(ctx context.Context, repo *repo_model.Repository, pr *issues_model.PullRequest, issue *issues_model.Issue, scope cairnmodels.DataScope) (PRContext, error) {
	out := PRContext{
		BaseBranch: pr.BaseBranch,
		HeadBranch: pr.HeadBranch,
	}
	if issue != nil {
		out.Title = issue.Title
		out.Body = issue.Content
	}

	gitRepo, err := git.OpenRepository(ctx, repo.RepoPath())
	if err != nil {
		return out, fmt.Errorf("open repo: %w", err)
	}
	defer gitRepo.Close()

	mergeBase, err := gitRepo.GetMergeBaseSimple(pr.BaseBranch, pr.HeadBranch)
	if err != nil {
		log.Warn("cairn summarizer: merge base for repo=%d pr=%d: %v", repo.ID, pr.Index, err)
		return out, nil
	}

	if files, ferr := gitRepo.GetFilesChangedBetween(mergeBase, pr.HeadBranch); ferr == nil {
		out.FilePaths = files
	} else {
		log.Warn("cairn summarizer: list changed files for repo=%d pr=%d: %v", repo.ID, pr.Index, ferr)
	}

	if scope == cairnmodels.DataScopeCommitMessages || scope == cairnmodels.DataScopeFull {
		if baseCommit, berr := gitRepo.GetCommit(mergeBase); berr == nil {
			if headCommit, herr := gitRepo.GetCommit(pr.HeadBranch); herr == nil {
				if commits, cerr := gitRepo.CommitsBetween(headCommit, baseCommit); cerr == nil {
					out.CommitMessages = make([]string, 0, len(commits))
					for _, c := range commits {
						out.CommitMessages = append(out.CommitMessages, c.CommitMessage)
					}
				} else {
					log.Warn("cairn summarizer: commits between for repo=%d pr=%d: %v", repo.ID, pr.Index, cerr)
				}
			}
		}
	}

	if scope == cairnmodels.DataScopeFull {
		lw := &limitWriter{remaining: maxAutoDiffBytes}
		if derr := gitRepo.GetDiffFromMergeBase(pr.BaseBranch, pr.HeadBranch, lw); derr == nil {
			out.Diff = lw.w.String()
			if lw.truncated {
				out.Diff += "\n\n[diff truncated at 512 KB]\n"
			}
		} else {
			log.Warn("cairn summarizer: diff for repo=%d pr=%d: %v", repo.ID, pr.Index, derr)
		}
	}

	return out, nil
}

// prNotifier is the Forgejo notifier hook that schedules summarization runs
// when a PR is opened or synchronized.
type prNotifier struct {
	notify.NullNotifier
	queue *queue
}

func (n *prNotifier) NewPullRequest(ctx context.Context, pr *issues_model.PullRequest, _ []*user_model.User) {
	n.handle(ctx, pr)
}

func (n *prNotifier) PullRequestSynchronized(ctx context.Context, _ *user_model.User, pr *issues_model.PullRequest) {
	n.handle(ctx, pr)
}

func (n *prNotifier) handle(ctx context.Context, pr *issues_model.PullRequest) {
	svc := Global()
	if svc == nil {
		return
	}
	if err := pr.LoadIssue(ctx); err != nil {
		log.Warn("cairn summarizer: LoadIssue: %v", err)
		return
	}
	if err := pr.LoadBaseRepo(ctx); err != nil {
		log.Warn("cairn summarizer: LoadBaseRepo: %v", err)
		return
	}
	repo := pr.BaseRepo
	if repo == nil {
		return
	}

	_, cfg, err := svc.resolver(repo.OwnerID)
	if err != nil {
		log.Warn("cairn summarizer: resolve owner=%d: %v", repo.OwnerID, err)
		return
	}
	if cfg == nil || !cfg.Enabled {
		return
	}

	scope := cairnmodels.DataScopeFull
	if repo.IsPrivate {
		consent := &cairnmodels.SummarizerRepoConsent{}
		has, cerr := svc.engine.Context(ctx).Where("repo_id = ?", repo.ID).Get(consent)
		if cerr != nil {
			log.Warn("cairn summarizer: load consent repo=%d: %v", repo.ID, cerr)
			return
		}
		if !has || !consent.Enabled {
			return
		}
		scope = consent.DataScope
		if !scope.IsValid() {
			scope = cairnmodels.DataScopeMetadata
		}
	}

	prCtx, err := BuildPRContextFromForgejo(ctx, repo, pr, pr.Issue, scope)
	if err != nil {
		log.Warn("cairn summarizer: build context repo=%d pr=%d: %v", repo.ID, pr.Index, err)
		return
	}

	n.queue.enqueue(Job{
		RepoID:   repo.ID,
		PRNumber: pr.Index,
		OwnerID:  repo.OwnerID,
		Context:  prCtx,
		Scope:    scope,
	})
}
