package devenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/mod/modfile"

	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/e2e"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/hostbuild"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/kube"
	"github.com/rosenhouse/capi-dagger-dev-env/internal/devenv/state"
)

// modulePath is devenv's own module, which it builds from.
const modulePath = "github.com/rosenhouse/capi-dagger-dev-env"

var errNoSource = errors.New("devenv builds from its own source; run it in a checkout of " + modulePath)

// Source returns the root of devenv's own module at or above dir.
func Source(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, err := hostbuild.ModuleRoot(dir)
	if err != nil {
		return "", errNoSource
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	if modfile.ModulePath(gomod) != modulePath {
		return "", errNoSource
	}
	return root, nil
}

// Test brings up an environment, checks it from the host, redeploys into it, and deletes its VM.
// Without o.Name, it picks a random name, and deletes the environment's state once the test passes.
func Test(ctx context.Context, o Options, out io.Writer) error {
	random := o.Name == ""
	if random {
		env, err := state.New(o.StateDir, "")
		if err != nil {
			return err
		}
		o.Name = env.Name
	}
	if err := test(ctx, o, random); err != nil {
		if random {
			return fmt.Errorf("%w\nclean up: %s", err, o.hint("down --purge --name "+o.Name))
		}
		return err
	}
	fmt.Fprintf(out, "Environment %s passed.\n", o.Name)
	return nil
}

func test(ctx context.Context, o Options, purge bool) error {
	e, err := Up(ctx, o)
	if err != nil {
		return err
	}
	if err := e.check(ctx); err != nil {
		return errors.Join(e.fail(ctx, err), e.Close())
	}
	_, err = e.Delete(ctx, purge)
	return errors.Join(err, e.Close())
}

// check verifies the environment, runs the end-to-end scenario and a redeploy, and reports container restarts.
func (e *Environment) check(ctx context.Context) error {
	err := e.Verify(ctx)
	if err == nil {
		err = errors.Join(e2e.KubectlWorks(ctx, e.MgmtKubeconfig), e2e.KubectlWorks(ctx, e.WorkloadKubeconfig))
	}
	if err == nil {
		err = e2e.GreetingReachesWorkloadCluster(ctx, e.MgmtKubeconfig, e.WorkloadKubeconfig, WorkloadNamespace, WorkloadCluster)
	}
	if err == nil {
		err = e.Redeploy(ctx, "redeploy-test")
	}
	if err == nil {
		err = e2e.HelloServesVersion(ctx, e.WorkloadKubeconfig, "redeploy-test")
	}
	if err == nil {
		e.progress("container restarts: management " + e.restarts(ctx, e.MgmtKubeconfig) + ", workload " + e.restarts(ctx, e.WorkloadKubeconfig))
	}
	return err
}

func (e *Environment) restarts(ctx context.Context, kubeconfig string) string {
	cs, err := kube.Client(kubeconfig)
	if err != nil {
		return err.Error()
	}
	n, err := kube.Restarts(ctx, cs)
	if err != nil {
		return err.Error()
	}
	return strconv.Itoa(n)
}

// Status writes a table of the environments under o.StateDir, then lists other devenv VMs on the host.
func Status(ctx context.Context, o Options, out io.Writer) error {
	machines, err := Machines(ctx, o)
	if err != nil {
		return err
	}
	envs, err := state.List(o.StateDir)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tVM\tMGMT API\tWORKLOAD API\tREGISTRY\tKUBECONFIGS")
	for _, env := range envs {
		var ports [3]string
		var kubeconfigs []string
		if env.Running(machines) {
			if p, err := env.ReadPorts(); err == nil {
				ports = [3]string{strconv.Itoa(p.MgmtAPI), strconv.Itoa(p.WorkloadAPI), strconv.Itoa(p.Registry)}
			}
			kubeconfigs, _ = filepath.Glob(filepath.Join(env.Dir, "*.kubeconfig"))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", env.Name, env.Status(machines), ports[0], ports[1], ports[2], strings.Join(kubeconfigs, " "))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	others := state.Unclaimed(envs, machines)
	if len(others) == 0 {
		return nil
	}
	fmt.Fprintln(out, "\nThese devenv VMs belong to other state dirs, or to deleted ones. Delete one with: smolvm machine delete -f --name <VM>")
	w = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, m := range others {
		fmt.Fprintf(w, "%s\t%s\n", m.Name, m.State)
	}
	return w.Flush()
}
