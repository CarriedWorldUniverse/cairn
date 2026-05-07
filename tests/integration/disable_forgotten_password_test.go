package integration

import (
	"net/http"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/test"
	"github.com/CarriedWorldUniverse/cairn/tests"
)

func TestDisableForgottenPasswordFalse(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Service.EnableInternalSignIn, true)()

	req := NewRequest(t, "GET", "/user/login/")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "a[href='/user/forgot_password']", true)
}

func TestDisableForgottenPasswordTrue(t *testing.T) {
	defer tests.PrepareTestEnv(t)()
	defer test.MockVariableValue(&setting.Service.EnableInternalSignIn, false)()

	req := NewRequest(t, "GET", "/user/login/")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "a[href='/user/forgot_password']", false)
}

func TestDisableForgottenPasswordDefault(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user/login/")
	resp := MakeRequest(t, req, http.StatusOK)
	htmlDoc := NewHTMLParser(t, resp.Body)
	htmlDoc.AssertElement(t, "a[href='/user/forgot_password']", true)
}
