// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"strings"
	"testing"

	"forgejo.org/tests"

	"github.com/PuerkitoBio/goquery"
	"github.com/stretchr/testify/assert"
)

func testIssueCommentChangeEvent(t *testing.T, htmlDoc *HTMLDoc, commentID string, texts, links []string) {
	event := htmlDoc.Find("#issuecomment-" + commentID + " .text")

	for _, text := range texts {
		assert.Contains(t, strings.Join(strings.Fields(event.Text()), " "), text)
	}

	var ids []string
	var hrefs []string
	event.Find("a").Each(func(i int, s *goquery.Selection) {
		if id, exists := s.Attr("id"); exists {
			ids = append(ids, id)
		}
		if href, exists := s.Attr("href"); exists {
			hrefs = append(hrefs, href)
		}
	})

	assert.Equal(t, []string{"event-" + commentID}, ids)

	issueCommentLink := "#issuecomment-" + commentID
	found := false
	for _, link := range links {
		if link == issueCommentLink {
			found = true
			break
		}
	}
	if !found {
		links = append(links, issueCommentLink)
	}
	assert.Equal(t, links, hrefs)
}

func TestIssueCommentChangeMilestone(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Add milestone
	testIssueCommentChangeEvent(t, htmlDoc, "2000",
		[]string{"user1 added this to the milestone1 milestone"},
		[]string{"/user1"})
	// []string{"/user1", "/user2/repo1/milestone/1"})

	// Modify milestone
	testIssueCommentChangeEvent(t, htmlDoc, "2001",
		[]string{"user1 modified the milestone from milestone1 to milestone2"},
		[]string{"/user1"})
	// []string{"/user1", "/user2/repo1/milestone/1", "/user2/repo1/milestone/2"})

	// Remove milestone
	testIssueCommentChangeEvent(t, htmlDoc, "2002",
		[]string{"user1 removed this from the milestone2 milestone"},
		[]string{"/user1"})
	// []string{"/user1", "/user2/repo1/milestone/2"})

	// Deleted milestone
	testIssueCommentChangeEvent(t, htmlDoc, "2003",
		[]string{"user1 added this to the (deleted) milestone"},
		[]string{"/user1"})
}

func TestIssueCommentChangeProject(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Add project
	testIssueCommentChangeEvent(t, htmlDoc, "2010",
		[]string{"user1 added this to the First project project"},
		[]string{"/user1"})
	// []string{"/user1", "/user2/repo1/projects/1"})

	// Modify project
	testIssueCommentChangeEvent(t, htmlDoc, "2011",
		[]string{"user1 modified the project from First project to second project"},
		[]string{"/user1"})
	// []string{"/user1", "/user2/repo1/projects/1", "/user2/repo1/projects/2"})

	// Remove project
	testIssueCommentChangeEvent(t, htmlDoc, "2012",
		[]string{"user1 removed this from the second project project"},
		[]string{"/user1"})
	// []string{"/user1", "/user2/repo1/projects/2"})

	// Deleted project
	testIssueCommentChangeEvent(t, htmlDoc, "2013",
		[]string{"user1 added this to the (deleted) project"},
		[]string{"/user1"})
}

func TestIssueCommentChangeLabel(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Add multiple labels
	testIssueCommentChangeEvent(t, htmlDoc, "2020",
		[]string{"user1 added the label1 label2 labels "},
		[]string{"/user1", "/user2/repo1/issues?labels=1", "/user2/repo1/issues?labels=2"})
	assert.Empty(t, htmlDoc.Find("#issuecomment-2021 .text").Text())

	// Remove single label
	testIssueCommentChangeEvent(t, htmlDoc, "2022",
		[]string{"user2 removed the label1 label "},
		[]string{"/user2", "/user2/repo1/issues?labels=1"})

	// Modify labels (add and remove)
	testIssueCommentChangeEvent(t, htmlDoc, "2023",
		[]string{"user1 added label1 and removed label2 labels "},
		[]string{"/user1", "/user2/repo1/issues?labels=1", "/user2/repo1/issues?labels=2"})
	assert.Empty(t, htmlDoc.Find("#issuecomment-2024 .text").Text())

	// Add single label
	testIssueCommentChangeEvent(t, htmlDoc, "2025",
		[]string{"user2 added the label2 label "},
		[]string{"/user2", "/user2/repo1/issues?labels=2"})

	// Remove multiple labels
	testIssueCommentChangeEvent(t, htmlDoc, "2026",
		[]string{"user1 removed the label1 label2 labels "},
		[]string{"/user1", "/user2/repo1/issues?labels=1", "/user2/repo1/issues?labels=2"})
	assert.Empty(t, htmlDoc.Find("#issuecomment-2027 .text").Text())
}

func TestIssueCommentChangeAssignee(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Self-assign
	testIssueCommentChangeEvent(t, htmlDoc, "2040",
		[]string{"user1 self-assigned this"},
		[]string{"/user1"})

	// Remove other
	testIssueCommentChangeEvent(t, htmlDoc, "2041",
		[]string{"user1 was unassigned by user2"},
		[]string{"/user1"})
	// []string{"/user1", "/user2"})

	// Add other
	testIssueCommentChangeEvent(t, htmlDoc, "2042",
		[]string{"user2 was assigned by user1"},
		[]string{"/user2"})
	// []string{"/user2", "/user1"})

	// Self-remove
	testIssueCommentChangeEvent(t, htmlDoc, "2043",
		[]string{"user2 removed their assignment"},
		[]string{"/user2"})
}

func TestIssueCommentChangeLock(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Lock without reason
	testIssueCommentChangeEvent(t, htmlDoc, "2050",
		[]string{"user1 locked and limited conversation to collaborators"},
		[]string{"/user1"})

	// Unlock
	testIssueCommentChangeEvent(t, htmlDoc, "2051",
		[]string{"user1 unlocked this conversation"},
		[]string{"/user1"})

	// Lock with reason
	testIssueCommentChangeEvent(t, htmlDoc, "2052",
		[]string{"user1 locked as Too heated and limited conversation to collaborators"},
		[]string{"/user1"})

	// Unlock
	testIssueCommentChangeEvent(t, htmlDoc, "2053",
		[]string{"user1 unlocked this conversation"},
		[]string{"/user1"})
}

func TestIssueCommentChangePin(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Pin
	testIssueCommentChangeEvent(t, htmlDoc, "2060",
		[]string{"user1 pinned this"},
		[]string{"/user1"})

	// Unpin
	testIssueCommentChangeEvent(t, htmlDoc, "2061",
		[]string{"user1 unpinned this"},
		[]string{"/user1"})
}

func TestIssueCommentChangeOpen(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Close issue
	testIssueCommentChangeEvent(t, htmlDoc, "2070",
		[]string{"user1 closed this issue"},
		[]string{"/user1"})

	// Reopen issue
	testIssueCommentChangeEvent(t, htmlDoc, "2071",
		[]string{"user2 reopened this issue"},
		[]string{"/user2"})

	req = NewRequest(t, "GET", "/user2/repo1/pulls/2")
	resp = MakeRequest(t, req, http.StatusOK)
	htmlDoc = NewHTMLParser(t, resp.Body)

	// Close pull request
	testIssueCommentChangeEvent(t, htmlDoc, "2072",
		[]string{"user1 closed this pull request"},
		[]string{"/user1"})

	// Reopen pull request
	testIssueCommentChangeEvent(t, htmlDoc, "2073",
		[]string{"user2 reopened this pull request"},
		[]string{"/user2"})
}

func TestIssueCommentChangeIssueReference(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/issues/1")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Issue reference from issue
	testIssueCommentChangeEvent(t, htmlDoc, "2080",
		[]string{"user1 referenced this issue ", "issue5 #4"},
		[]string{"/user1", "/user2/repo1/issues/4", "#issuecomment-2080", "/user2/repo1/issues/4"})

	// Issue reference from pull
	testIssueCommentChangeEvent(t, htmlDoc, "2081",
		[]string{"user1 referenced this issue ", "issue2 #2"},
		[]string{"/user1", "/user2/repo1/pulls/2", "#issuecomment-2081", "/user2/repo1/pulls/2"})

	// Issue reference from issue in different repo
	testIssueCommentChangeEvent(t, htmlDoc, "2082",
		[]string{"user1 referenced this issue from org3/repo21", "just a normal issue #1"},
		[]string{"/user1", "/org3/repo21/issues/1", "#issuecomment-2082", "/org3/repo21/issues/1"})

	// Issue reference from pull in different repo
	testIssueCommentChangeEvent(t, htmlDoc, "2083",
		[]string{"user1 referenced this issue from user12/repo10 ", "pr2 #1"},
		[]string{"/user1", "/user12/repo10/pulls/1", "#issuecomment-2083", "/user12/repo10/pulls/1"})
}

func TestIssueCommentChangePullReference(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user2/repo1/pulls/2")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)

	// Pull reference from issue
	testIssueCommentChangeEvent(t, htmlDoc, "2090",
		[]string{"user1 referenced this pull request ", "issue1 #1"},
		[]string{"/user1", "/user2/repo1/issues/1", "#issuecomment-2090", "/user2/repo1/issues/1"})

	// Pull reference from pull
	testIssueCommentChangeEvent(t, htmlDoc, "2091",
		[]string{"user1 referenced this pull request ", "issue2 #2"},
		[]string{"/user1", "/user2/repo1/pulls/2", "#issuecomment-2091", "/user2/repo1/pulls/2"})

	// Pull reference from issue in different repo
	testIssueCommentChangeEvent(t, htmlDoc, "2092",
		[]string{"user1 referenced this pull request from org3/repo21", "just a normal issue #1"},
		[]string{"/user1", "/org3/repo21/issues/1", "#issuecomment-2092", "/org3/repo21/issues/1"})

	// Pull reference from pull in different repo
	testIssueCommentChangeEvent(t, htmlDoc, "2093",
		[]string{"user1 referenced this pull request from user12/repo10 ", "pr2 #1"},
		[]string{"/user1", "/user12/repo10/pulls/1", "#issuecomment-2093", "/user12/repo10/pulls/1"})
}
