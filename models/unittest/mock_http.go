// Copyright 2017 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package unittest

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"code.gitea.io/gitea/modules/log"

	"github.com/stretchr/testify/assert"
)

// Mocks HTTP responses of a third-party service (such as GitHub, GitLab…)
// This has two modes:
//   - live mode: the requests made to the mock HTTP server are transmitted to the live
//     service, and responses are saved as test data files
//   - test mode: the responses to requests to the mock HTTP server are read from the
//     test data files
func NewMockWebServer(t *testing.T, liveServerBaseURL, testDataDir string, liveMode bool) *httptest.Server {
	mockServerBaseURL := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := NormalizedFullPath(r.URL)
		log.Info("Mock HTTP Server: got request for path %s", r.URL.Path)
		// TODO check request method (support POST?)
		fixturePath := fmt.Sprintf("%s/%s", testDataDir, strings.NewReplacer("/", "_", "?", "!").Replace(path))
		if liveMode {
			liveURL := fmt.Sprintf("%s%s", liveServerBaseURL, path)

			request, err := http.NewRequest(r.Method, liveURL, nil)
			if err != nil {
				assert.Fail(t, "constructing an HTTP request to %s failed", liveURL, err)
			}
			for headerName, headerValues := range r.Header {
				// do not pass on the encoding: let the Transport of the HTTP client handle that for us
				if strings.ToLower(headerName) != "accept-encoding" {
					for _, headerValue := range headerValues {
						request.Header.Add(headerName, headerValue)
					}
				}
			}

			response, err := http.DefaultClient.Do(request)
			if err != nil {
				assert.Fail(t, "HTTP request to %s failed: %s", liveURL, err)
			}

			fixture, err := os.Create(fixturePath)
			if err != nil {
				assert.Fail(t, fmt.Sprintf("failed to open the fixture file %s for writing", fixturePath), err)
			}
			defer fixture.Close()
			fixtureWriter := bufio.NewWriter(fixture)

			for headerName, headerValues := range response.Header {
				for _, headerValue := range headerValues {
					if strings.ToLower(headerName) != "host" {
						_, err := fixtureWriter.WriteString(fmt.Sprintf("%s: %s\n", headerName, headerValue))
						if err != nil {
							assert.Fail(t, "writing the header of the HTTP response to the fixture file failed", err)
						}
					}
				}
			}
			if _, err := fixtureWriter.WriteString("\n"); err != nil {
				assert.Fail(t, "writing the header of the HTTP response to the fixture file failed")
			}
			fixtureWriter.Flush()

			reader := response.Body
			content, err := io.ReadAll(reader)
			if err != nil {
				assert.Fail(t, "reading the response of the HTTP request to %s failed: %s", liveURL, err)
			}
			log.Info("Mock HTTP Server: writing response to %s", fixturePath)
			if _, err := fixture.Write(content); err != nil {
				assert.Fail(t, "writing the body of the HTTP response to the fixture file failed", err)
			}

			if err := fixture.Sync(); err != nil {
				assert.Fail(t, "writing the body of the HTTP response to the fixture file failed", err)
			}
		}

		fixture, err := os.ReadFile(fixturePath)
		if err != nil {
			assert.Fail(t, "missing mock HTTP response: "+fixturePath)
			return
		}

		w.WriteHeader(http.StatusOK)

		// parse back the fixture file into a series of HTTP headers followed by response body
		lines := strings.Split(string(fixture), "\n")
		for idx, line := range lines {
			colonIndex := strings.Index(line, ": ")
			if colonIndex != -1 {
				w.Header().Set(line[0:colonIndex], line[colonIndex+2:])
			} else {
				// we reached the end of the headers (empty line), so what follows is the body
				responseBody := strings.Join(lines[idx+1:], "\n")
				// replace any mention of the live HTTP service by the mocked host
				responseBody = strings.ReplaceAll(responseBody, liveServerBaseURL, mockServerBaseURL)
				if _, err := w.Write([]byte(responseBody)); err != nil {
					assert.Fail(t, "writing the body of the HTTP response failed", err)
				}
				break
			}
		}
	}))
	mockServerBaseURL = server.URL
	return server
}

func NormalizedFullPath(url *url.URL) string {
	// TODO normalize path (remove trailing slash?)
	// TODO normalize RawQuery (order query parameters?)
	if len(url.Query()) == 0 {
		return url.EscapedPath()
	}
	return fmt.Sprintf("%s?%s", url.EscapedPath(), url.RawQuery)
}
