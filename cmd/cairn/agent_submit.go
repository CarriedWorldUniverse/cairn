package cairn

import (
	"context"
	"fmt"
	"io"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

// AgentSubmit posts a registration request as the authed user
// (token from <hostdir>/token). Auto-approve happens server-side
// when the auth user matches proposed_owner.
func AgentSubmit(instanceURL, proposedOwner, slug, domain string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}
	return submit(instanceURL, token, proposedOwner, slug, domain, paths, out)
}

// AgentSubmitAnonymous posts a registration without a token. The
// resulting agent goes to pending status; the proposed owner must
// approve via web UI, cairn agents approve, or the API directly.
func AgentSubmitAnonymous(instanceURL, proposedOwner, slug, domain string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	return submit(instanceURL, "", proposedOwner, slug, domain, paths, out)
}

func submit(instanceURL, token, proposedOwner, slug, domain string, paths *Paths, out io.Writer) error {
	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}
	_, pub, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}

	c := NewClient(instanceURL, token)
	resp, err := c.PostAgent(context.Background(), PostAgentRequest{
		ProposedOwner: proposedOwner,
		Slug:          slug,
		Domain:        domain,
		PublicKey:     pub,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "fingerprint: %s\n", resp.Fingerprint)
	fmt.Fprintf(out, "status: %s\n", resp.Status)
	if resp.Status == "pending" {
		fmt.Fprintf(out, "note: awaiting %s's approval — owner can run `cairn agents approve %s`\n",
			proposedOwner, resp.Fingerprint)
	}
	return nil
}
