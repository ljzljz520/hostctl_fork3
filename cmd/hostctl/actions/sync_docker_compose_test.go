package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/client"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/guumaster/hostctl/pkg/docker"
	"github.com/guumaster/hostctl/pkg/types"
)

func Test_SyncDockerCompose(t *testing.T) {
	cmd := NewRootCmd()

	r := NewRunner(t, cmd, "remove")
	defer r.Clean()

	cli := prepareComposeCli(t, r)

	opts := testGetOptions(t, cli)
	cmdSync := newSyncDockerCmd(nil, opts)
	cmdSync.Use = "test-sync-docker-compose"

	cmd.AddCommand(cmdSync)

	r.Run("hostctl test-sync-docker-compose profile2").
		Containsf(`
				[ℹ] Using hosts file: %s

        +----------+--------+-----------+------------------------------+
        | PROFILE  | STATUS |    IP     |            DOMAIN            |
        +----------+--------+-----------+------------------------------+
        | profile2 | on     | 172.0.0.2 | testing-app_container1_1.loc |
        | profile2 | on     | 172.0.0.3 | testing-app_container2_1.loc |
        | profile2 | on     | 172.0.0.4 | testing-app_db.loc           |
        +----------+--------+-----------+------------------------------+
			`, r.Hostfile())
}

// Test_SyncDockerComposeAttribution exercises the real sync docker-compose
// command end to end: compose file parsing, project selection, compose
// labels and domain/prefix flags.
func Test_SyncDockerComposeAttribution(t *testing.T) {
	cli, composePath := prepareLabelledComposeCli(t)

	t.Run("labels keep similar projects apart", func(t *testing.T) {
		cmd := NewRootCmd()
		r := NewRunner(t, cmd, "compose-labels")
		defer r.Clean()

		cmdSync := newSyncDockerComposeCmd(nil, composeOptions(cli))
		cmdSync.Use = "test-compose"
		addComposeTestFlags(cmdSync)
		cmd.AddCommand(cmdSync)

		r.Run("hostctl test-compose app --project-name app --compose-file " + composePath)

		content, err := os.ReadFile(r.Hostfile())
		require.NoError(t, err)

		hosts := string(content)
		assert.Contains(t, hosts, "172.0.0.2 web-1.loc")
		assert.Contains(t, hosts, "172.0.0.3 webcache-1.loc")
		assert.NotContains(t, hosts, "172.0.0.4")
		assert.NotContains(t, hosts, "172.0.0.6")
	})

	t.Run("--prefix and --domain are preserved", func(t *testing.T) {
		cmd := NewRootCmd()
		r := NewRunner(t, cmd, "compose-flags")
		defer r.Clean()

		cmdSync := newSyncDockerComposeCmd(nil, composeOptions(cli))
		cmdSync.Use = "test-compose-flags"
		addComposeTestFlags(cmdSync)
		cmd.AddCommand(cmdSync)

		r.Run("hostctl test-compose-flags app --project-name app " +
			"--compose-file " + composePath + " --prefix --domain app.dev")

		content, err := os.ReadFile(r.Hostfile())
		require.NoError(t, err)

		hosts := string(content)
		assert.Contains(t, hosts, "172.0.0.2 app-web-1.app.dev")
		assert.Contains(t, hosts, "172.0.0.3 app-webcache-1.app.dev")
		assert.NotContains(t, hosts, "172.0.0.4")
	})

	t.Run("ambiguous compose file aborts before replacing hosts", func(t *testing.T) {
		cmd := NewRootCmd()
		r := NewRunner(t, cmd, "compose-ambiguous")
		defer r.Clean()

		before, err := os.ReadFile(r.Hostfile())
		require.NoError(t, err)

		dir := t.TempDir()
		badPath := filepath.Join(dir, "docker-compose.yml")
		require.NoError(t, os.WriteFile(badPath, []byte(`
version: "3"
services:
  web:
    container_name: dup
  webcache:
    container_name: dup
`), 0o600))

		cmdSync := newSyncDockerComposeCmd(nil, composeOptions(cli))
		cmdSync.Use = "test-compose-bad"
		addComposeTestFlags(cmdSync)
		cmd.AddCommand(cmdSync)

		r.RunE("hostctl test-compose-bad app --project-name app --compose-file "+badPath,
			fmt.Errorf("%w: services %q and %q both declare container_name %q",
				types.ErrAmbiguousComposeContainer, "web", "webcache", "dup"))

		after, err := os.ReadFile(r.Hostfile())
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})
}

func addComposeTestFlags(c *cobra.Command) {
	c.Flags().String("network", "", "")
	c.Flags().StringP("domain", "d", "loc", "")
	c.Flags().String("compose-file", "", "")
	c.Flags().String("project-name", "", "")
	c.Flags().Bool("prefix", false, "")
}

func composeOptions(cli *client.Client) getOptionsFn {
	return func(cmd *cobra.Command, profiles []string) (*docker.Options, error) {
		opts, err := defaultGetOptions(cmd, profiles)
		if err != nil {
			return nil, err
		}

		opts.Cli = cli

		return opts, nil
	}
}

func prepareComposeCli(t *testing.T, r Runner) *client.Client {
	c := r.TempHostfile("docker-compose.yml")
	defer os.Remove(c.Name())

	_, _ = c.WriteString(`
version: "3"
services:

  container1:
    image: some_image:3.5
    networks:
      - networkName1

  container2:
    image: some_other_image:1.0
    networks:
      - networkName1

  another-thing:
    container_name: db
    image: db:1.0
    networks:
      - networkName1

networks:
  networkName1:
`)

	dockerResponse := map[string]string{
		"/v1.22/networks": `[
{"Id": "testing-app_networkID1", "Name": "testing-app_networkName1" }
]`,
		"/v1.22/containers/json": `[{
	"Id": "container_id1", "Names": ["/testing-app_container1_1"],
	"NetworkSettings": {
		"Networks": { "testing-app_networkName1": { "NetworkID": "testing-app_networkID1", "IPAddress": "172.0.0.2" }}
  }
},{
	"Id": "container_id2", "Names": ["/testing-app_container2_1"],
	"NetworkSettings": {
		"Networks": { "testing-app_networkName1": { "NetworkID": "testing-app_networkID1", "IPAddress": "172.0.0.3" }}
  }
},{
	"Id": "container_id_db", "Names": ["/testing-app_db"],
	"NetworkSettings": {
		"Networks": { "testing-app_networkName1": { "NetworkID": "testing-app_networkID1", "IPAddress": "172.0.0.4" }}
  }
}]`,
	}

	return newClientWithResponse(t, dockerResponse)
}

func prepareLabelledComposeCli(t *testing.T) (*client.Client, string) {
	t.Helper()

	dir := t.TempDir()
	composePath := filepath.Join(dir, "docker-compose.yml")

	require.NoError(t, os.WriteFile(composePath, []byte(`
version: "3"
services:
  web:
    image: nginx
  webcache:
    image: redis
`), 0o600))

	dockerResponse := map[string]string{
		"/v1.22/containers/json": `[
  {
    "Id": "web1", "Names": ["/app-web-1"],
    "Labels": { "com.docker.compose.project": "app", "com.docker.compose.service": "web" },
    "NetworkSettings": { "Networks": { "default": { "NetworkID": "net1", "IPAddress": "172.0.0.2" }}}
  },
  {
    "Id": "cache1", "Names": ["/app-webcache-1"],
    "Labels": { "com.docker.compose.project": "app", "com.docker.compose.service": "webcache" },
    "NetworkSettings": { "Networks": { "default": { "NetworkID": "net1", "IPAddress": "172.0.0.3" }}}
  },
  {
    "Id": "myapp", "Names": ["/myapp_web_1"],
    "Labels": { "com.docker.compose.project": "myapp", "com.docker.compose.service": "web" },
    "NetworkSettings": { "Networks": { "default": { "NetworkID": "net1", "IPAddress": "172.0.0.4" }}}
  },
  {
    "Id": "appx", "Names": ["/appx-web-1"],
    "Labels": { "com.docker.compose.project": "appx", "com.docker.compose.service": "web" },
    "NetworkSettings": { "Networks": { "default": { "NetworkID": "net1", "IPAddress": "172.0.0.6" }}}
  }
]`,
	}

	return newClientWithResponse(t, dockerResponse), composePath
}
