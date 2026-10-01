package infra

import "testing"

func TestSessionDomainSkipsHostSearchDomains(t *testing.T) {
	resolvConf := "nameserver 10.87.0.1\nsearch drhoi2nbjkse1ck2abdqzbxyjc.dx.internal.cloudapp.net 6okdafa1j2eis.dagger.local dagger.local\n"

	got, err := sessionDomain(resolvConf)

	if err != nil || got != "6okdafa1j2eis.dagger.local" {
		t.Errorf("sessionDomain() = %q, %v", got, err)
	}
}

func TestSessionDomainFailsWithoutDaggerDomain(t *testing.T) {
	if _, err := sessionDomain("search example.com\n"); err == nil {
		t.Error("no error")
	}
}

func TestHostsTOMLTriesMirrorThenServer(t *testing.T) {
	got := hostsTOML("https://registry-1.docker.io", "mirror.abc.dagger.local:5000")
	want := `server = "https://registry-1.docker.io"

[host."http://mirror.abc.dagger.local:5000"]
  capabilities = ["pull", "resolve"]
`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
