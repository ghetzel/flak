package main

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghetzel/cli"
	"github.com/ghetzel/go-stockutil/executil"
	"github.com/ghetzel/go-stockutil/fileutil"
	"github.com/ghetzel/go-stockutil/log"
	"github.com/ghetzel/go-stockutil/rxutil"
	"github.com/ghetzel/go-stockutil/sliceutil"
	"github.com/ghetzel/go-stockutil/stringutil"
)

type sshResults struct {
	Hostname    string    `json:"hostname"`
	Error       error     `json:"error,omitempty"`
	Status      int       `json:"status,omitempty"`
	Stdout      []string  `json:"stdout,omitempty"`
	Stderr      []string  `json:"stderr,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

func (self *sshResults) Duration() time.Duration {
	return self.CompletedAt.Sub(self.StartedAt)
}

func (self *sshResults) String() string {
	if self.Error != nil {
		return self.Error.Error()
	} else {
		return fmt.Sprintf("exited status %d in %v", self.Status, self.Duration().Round(time.Millisecond))
	}
}

func main() {
	app := cli.NewApp()
	app.Name = `flak`
	app.Usage = `A big stupid for-loop for running SSH commands`
	app.ArgsUsage = `COMMAND`
	app.Version = Version

	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  `hosts, H`,
			Usage: `Specify a filename containing [user@]host:port pairs to connect to.`,
		},
		cli.StringFlag{
			Name:   `log-level, L`,
			Usage:  `Level of log output verbosity`,
			Value:  `notice`,
			EnvVar: `LOGLEVEL`,
		},
		cli.StringFlag{
			Name:  `format, f`,
			Usage: `Specify the output format for data output from subcommands.`,
			Value: `json`,
		},
		cli.StringFlag{
			Name:  `scp-bin`,
			Usage: `Specify the name of the "scp" binary to use for copying files.`,
			Value: `scp`,
		},
		cli.StringFlag{
			Name:  `ssh-bin`,
			Usage: `Specify the name of the "ssh" binary to use for copying files.`,
			Value: `ssh`,
		},
		cli.StringFlag{
			Name:  `ssh-config-file, F`,
			Usage: `Specify the SSH configuration file to use.`,
		},
		cli.DurationFlag{
			Name:  `connect-timeout, t`,
			Usage: `Specify the connection timeout for each SSH connection.`,
			Value: 10 * time.Second,
		},
		cli.StringSliceFlag{
			Name:  `ssh-option, o`,
			Usage: `Specify an SSH command line option in the form "-o Key=Value"`,
		},
	}

	app.Before = func(c *cli.Context) error {
		log.SetLevelString(c.String(`log-level`))
		return nil
	}

	app.Action = func(c *cli.Context) {
		if c.NArg() == 0 {
			log.Fatalf("Must specify a command or @filename to run")
			return
		}

		if hosts, err := parseHosts(c); err == nil {
			var wg sync.WaitGroup
			var script string
			var multiline bool

			if scriptfile := c.Args().First(); strings.HasPrefix(scriptfile, `@`) {
				scriptfile = strings.TrimPrefix(scriptfile, `@`)

				if data, err := fileutil.ReadAll(scriptfile); err == nil {
					script = string(data)
					multiline = true
				} else {
					log.Fatalf("failed to read %q: %v", scriptfile, err)
					return
				}
			} else {
				script = strings.Join(c.Args(), ` `)
			}

			script = strings.TrimSpace(script)

			if script == `` {
				log.Fatalf("Must specify a command or @filename to run")
				return
			}

			sort.Strings(hosts)
			hosts = sliceutil.UniqueStrings(hosts)

			resultchan := make(chan *sshResults, len(hosts))
			results := make([]*sshResults, 0)

			for _, host := range hosts {
				wg.Add(1)
				go sshexec(&wg, resultchan, c, host, nil, script, multiline)
			}

			go func() {
				for result := range resultchan {
					results = append(results, result)
				}
			}()

			wg.Wait()

			sort.Slice(results, func(i int, j int) bool {
				return results[i].Hostname < results[j].Hostname
			})

			for _, result := range results {
				lvl := log.INFO

				if result.Error != nil {
					lvl = log.ERROR
				} else if result.Status != 0 {
					lvl = log.WARNING
				}

				log.Logf(lvl, "[res|%s] %v", result.Hostname, result)
			}
		} else {
			log.Fatal(err)
		}
	}

	app.Run(os.Args)
}

func parseHosts(c *cli.Context) ([]string, error) {
	var data []byte
	var hosts []string
	var lines []string

	if hostfile := c.String(`hosts`); hostfile != `` {
		if d, err := fileutil.ReadAll(hostfile); err == nil {
			data = d
		} else {
			return nil, fmt.Errorf("cannot read input: %v", err)
		}
	} else if d, err := ioutil.ReadAll(os.Stdin); err == nil {
		data = d
	} else {
		return nil, fmt.Errorf("cannot read input: %v", err)
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("no hosts provided via flag or standard input")
	}

	if ifs := os.Getenv(`IFS`); ifs != `` {
		lines = strings.Split(string(data), ifs)
	} else {
		lines = rxutil.Whitespace.Split(string(data), -1)
	}

	for _, line := range lines {
		if line := strings.TrimSpace(line); line == `` {
			continue
		} else if strings.HasPrefix(line, `#`) {
			continue
		} else {
			hosts = append(hosts, line)
		}
	}

	return hosts, nil
}

func sshexec(
	wg *sync.WaitGroup,
	reschan chan *sshResults,
	c *cli.Context,
	hostname string,
	env map[string]interface{},
	script string,
	multiline bool,
) {
	results := new(sshResults)

	defer func() {
		reschan <- results
		wg.Done()
	}()

	var sshTo *executil.Cmd

	scp := c.String(`scp-bin`)
	ssh := c.String(`ssh-bin`)
	flags := make([]string, 0)
	hostname, port := stringutil.SplitPair(hostname, `:`)
	remoteFile := ``

	if multiline {
		remoteFile = fmt.Sprintf("flak-%d-%d", time.Now().UnixNano(), rand.Intn(65536))

		// have the script remove itself as the final command
		script += fmt.Sprintf("\nret=$?; rm -f '%s'; exit $ret\n", remoteFile)
	}

	if port != `` {
		flags = append(flags, `-q`, `-o`, `Port=`+port)
	}

	flags = append(flags, `-q`, `-o`, `BatchMode=yes`)

	if cf := c.String(`ssh-config-file`); cf != `` {
		flags = append(flags, `-F`, fileutil.MustExpandUser(cf))
	}

	if ct := c.Duration(`connect-timeout`).Round(time.Second); ct > 0 {
		flags = append(flags, `-o`, fmt.Sprintf("ConnectTimeout=%d", ct/time.Second))
	}

	for _, pair := range c.StringSlice(`ssh-option`) {
		pair = strings.TrimSpace(pair)

		if pair != `` {
			flags = append(flags, `-o`, pair)
		}
	}

	results.StartedAt = time.Now()
	results.Hostname = hostname

	if multiline {
		if filename, err := fileutil.WriteTempFile(script, ``); err == nil {
			scpTo := executil.Command(scp, append(flags, filename, fmt.Sprintf("%s:%s", hostname, remoteFile))...)

			for k, v := range env {
				scpTo.SetEnv(k, v)
			}

			scpTo.OnStdout = cmdlog(hostname, nil)
			scpTo.OnStderr = cmdlog(hostname, nil)

			log.Debugf("[%s] exec: %s", hostname, strings.Join(scpTo.Args, ` `))

			if err := scpTo.Run(); err == nil {

			} else {
				results.Error = fmt.Errorf("script scp: %v", err)
				return
			}
		} else {
			results.Error = fmt.Errorf("failed to write script file: %v", err)
			return
		}

		sshTo = executil.Command(ssh, append(flags, hostname, `./`+remoteFile)...)
	} else {
		sshTo = executil.Command(ssh, append(flags, hostname, script)...)
	}

	for k, v := range env {
		sshTo.SetEnv(k, v)
	}

	tag := fmt.Sprintf("%s\t", hostname)
	sshTo.OnStdout = cmdlog(tag, results)
	sshTo.OnStderr = cmdlog(tag, results)

	log.Debugf("[%s] exec: %s", tag, strings.Join(sshTo.Args, ` `))

	results.Error = sshTo.Run()
	status := sshTo.WaitStatus()

	results.Status = status.ExitCode
	results.CompletedAt = time.Now()
	return
}

func cmdlog(tag string, results *sshResults) executil.OutputLineFunc {
	return func(line string, serr bool) {
		lvl := log.INFO

		if rxutil.Match(`(?i)(error|fail|critical|danger)`, line) != nil {
			lvl = log.ERROR
		} else if rxutil.Match(`(?i)(warn|alert)`, line) != nil {
			lvl = log.WARNING
		} else if rxutil.Match(`(?i)(note|notice)`, line) != nil {
			lvl = log.NOTICE
		} else if rxutil.Match(`(?i)(debug)`, line) != nil {
			lvl = log.DEBUG
		}

		if results != nil {
			if serr {
				results.Stderr = append(results.Stderr, line)
				log.Logf(lvl, "[%s] %s", tag, line)
			} else {
				results.Stdout = append(results.Stderr, line)
				fmt.Printf("%s\t%s\n", tag, line)
			}
		}
	}
}
