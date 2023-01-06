package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/user"
	"release"
	"strings"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	go_git_ssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	flag "github.com/spf13/pflag"
	"golang.org/x/crypto/ssh"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	incrementFormat = "%03d"
)

var version = "dev"

func loadKeys(path string) transport.AuthMethod {
	var auth transport.AuthMethod
	sshKey, _ := ioutil.ReadFile(path)
	signer, _ := ssh.ParsePrivateKey([]byte(sshKey))
	auth = &go_git_ssh.PublicKeys{User: "git", Signer: signer}
	return auth
}

func homeDir() string {
	usr, err := user.Current()
	if err != nil {
		log.Fatal().Err(err).Msg("unable to load home dir")
	}
	return usr.HomeDir
}

func getVersionString() string {
	return fmt.Sprintf("release %s", version)
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: release [component] [options]\n\n")
	flag.PrintDefaults()
}

func main() {

	modules := []string{}
	var remote, message string
	var verbose, dryRun, doPush, semVer, incMajor, incMinor, incPatch bool
	var user, email, sshKeyPath string
	format := "%Y.%m."
	defaultRemote := "origin"
	flag.StringArrayVarP(&modules, "component", "c", []string{}, "component to release, if not set will use 'release' which triggers all components to build and deploy, can also be specified as the first argument")
	flag.StringVarP(&remote, "remote", "r", defaultRemote, "git remote to push to (if --push)")
	flag.StringVarP(&message, "msg", "m", "", "optional release message, will create an annotated git tag")
	flag.StringVar(&user, "user", "", "override user in ~/.gitconfig")
	flag.StringVar(&email, "email", "", "override email in ~/.gitconfig")
	// flag.StringVarP(&format, "fmt", "f", "%Y.%m.", "date format to use")
	flag.BoolVar(&semVer, "semver", false, "use semantic versioning <major>.<minor>.<patch>-<rc>")
	flag.BoolVar(&incMajor, "inc-major", false, "increment major version of semantic version")
	flag.BoolVar(&incMinor, "inc-minor", false, "increment minor version of semantic version")
	flag.BoolVar(&incPatch, "inc-patch", false, "increment patch version of semantic version")
	flag.BoolVarP(&verbose, "verbose", "v", false, "enable more output")
	flag.BoolVar(&doPush, "push", false, "push tag to default remote (does 'git push')")
	flag.BoolVarP(&dryRun, "dry-run", "n", false, "don't create a release, just print what would be released")
	defaultSSHKeyPath := fmt.Sprintf("%s/.ssh/id_rsa", homeDir())
	flag.StringVar(&sshKeyPath, "ssh-key", defaultSSHKeyPath, "specify path to ssh key")
	showVersion := flag.Bool("version", false, "display the version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Fprintf(os.Stderr, "%s\n", getVersionString())
		os.Exit(0)
	}

	for idx := 0; idx < len(flag.Args()); idx++ {
		modules = append(modules, flag.Arg(idx))
	}

	if len(modules) == 0 {
		modules = append(modules, "")
	}

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	// If we want UTC use this
	// zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if verbose {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	}

	cfg, err := config.LoadConfig(config.GlobalScope)
	if err == nil {
		if user == "" {
			user = cfg.User.Name
		}
		if email == "" {
			email = cfg.User.Email
		}
	} else {
		// At this point, we might be in a CI environment and might not have gitconfig
		// setup. If we're not using heavy tags, we don't even care about this error,
		// so we'll log a warning (only visible at debug) and if the user tries to create
		// an annotated tag, we'll deal with it then.
		log.Debug().Err(err).Msg("unable to load git config, this is only a problem if you're using annotated tags")
	}

	cwd, err := os.Getwd()
	release.CheckIfError(err, "failed to get current dir")

	// Create a new Release Manager
	rm, err := release.NewManager(cwd, format, incrementFormat)

	if doPush {
		err := rm.CheckRemote(remote)
		release.CheckIfError(err, fmt.Sprintf("problem with remote '%s', cannot push, omit --push or fix the remote", remote))
	}

	// This is customizable, but for now, we always want a release number
	rm.AlwaysIncludeNumber = true

	release.CheckIfError(err, "failed to load release manager")

	newReleases := []string{}
	if semVer {
		proposedSemVer := rm.GetProposedSemName()
		proposedSemVer.IncrementVersion(incMajor, incMinor, incPatch)
		for _, module := range modules {
			branch, err := rm.GetBranch()
			if err != nil {
				log.Fatal().Msgf("Unable to get current branch: %s", err.Error())
			}
			newReleases = append(newReleases, proposedSemVer.FormatRelease(module, branch))
		}
	} else {
		proposedDate := rm.GetProposedDate()
		for _, module := range modules {
			if module == "" {
				newReleases = append(newReleases, proposedDate)
			} else {
				newReleases = append(newReleases, fmt.Sprintf("%s-%s", proposedDate, module))
			}
		}
	}

	plural := ""
	if len(newReleases) > 1 {
		plural = "s"
	}
	if dryRun {
		fmt.Printf("would create release%s:\n%s\n", plural, strings.Join(newReleases, ", "))
		os.Exit(0)
	}

	failedCreate := false
	for _, newRelease := range newReleases {
		_, err = rm.CreateTag(newRelease, message, user, email)
		if err != nil {
			log.Error().Msgf("failed to create tag %s: %s", newRelease, err.Error())
			failedCreate = true
			continue
		}
		// Success!
		fmt.Printf("created release: %s\n", newRelease)

		if doPush {
			msg, err := rm.PushTagToRemote(newRelease, remote, loadKeys(sshKeyPath))
			if err == nil {
				// Great Success!
				fmt.Println(msg)
			} else {
				log.Error().Err(err).Msg(msg)
				fmt.Printf("the tag will still be in the local repo you can delete it with `git tag -d %s` or push it with `git push <REMOTE> %s` once you have resolved the issue preventing push\n", newRelease, newRelease)
				failedCreate = true
			}
		}
	}
	if failedCreate {
		// We failed at least one create, exit
		pushMsg := ""
		if doPush {
			pushMsg = "/push"
		}
		log.Fatal().Msgf("at least one tag failed to create%s, see above. exiting...", pushMsg)
		os.Exit(1)
	}

	if !doPush {
		fmt.Printf("tag%s (%s) not pushed (--push not set), push it with:\n", plural, strings.Join(newReleases, ", "))
		fmt.Printf(" git push %s %s\n", remote, strings.Join(newReleases, " "))
	}
}
