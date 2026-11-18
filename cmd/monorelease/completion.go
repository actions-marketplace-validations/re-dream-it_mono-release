package main

import (
	"fmt"
	"strings"
)

// commands and flags offered by the completion scripts
var (
	commandList = "plan release version manifest changed explain notes completion help"
	flagList    = "-c -C --branch --ref --json --stable --component --push --remote --force --base --with-dependents --write --all --no-notes --no-releases"
)

func cmdCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: monorelease completion <bash|zsh|fish>")
	}
	switch args[0] {
	case "bash":
		fmt.Print(bashCompletion)
	case "zsh":
		fmt.Print(zshCompletion)
	case "fish":
		fmt.Print(fishCompletion)
	default:
		return fmt.Errorf("unknown shell %q (bash, zsh or fish)", args[0])
	}
	return nil
}

var bashCompletion = `# bash completion for monorelease. Load it with:  source <(monorelease completion bash)
_monorelease() {
  local cur prev cmd
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  cmd="${COMP_WORDS[1]}"
  if [[ $COMP_CWORD -eq 1 ]]; then
    COMPREPLY=($(compgen -W "` + commandList + `" -- "$cur"))
    return
  fi
  case "$prev" in
    -c) COMPREPLY=($(compgen -f -- "$cur")); return ;;
    -C) COMPREPLY=($(compgen -d -- "$cur")); return ;;
    --component|--force) COMPREPLY=($(compgen -W "$(monorelease __names 2>/dev/null)" -- "$cur")); return ;;
  esac
  if [[ "$cur" == -* ]]; then
    COMPREPLY=($(compgen -W "` + flagList + `" -- "$cur"))
    return
  fi
  case "$cmd" in
    version|manifest|explain|notes) COMPREPLY=($(compgen -W "$(monorelease __names 2>/dev/null)" -- "$cur")) ;;
    completion) COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur")) ;;
  esac
}
complete -F _monorelease monorelease
`

var zshCompletion = `#compdef monorelease
# zsh completion for monorelease. Put it in a directory on $fpath as "_monorelease",
# or load it with:  source <(monorelease completion zsh)
_monorelease() {
  local -a commands names
  commands=(
    'plan:show what would be released'
    'release:create tags (and releases with --push)'
    'version:latest released version'
    'manifest:umbrella members'
    'changed:components with changes'
    'explain:why a component is or is not released'
    'notes:release notes'
    'completion:print a shell completion script'
    'help:show help'
  )
  if (( CURRENT == 2 )); then
    _describe 'command' commands
    return
  fi
  if [[ $words[CURRENT] == -* ]]; then
    compadd -- ` + flagList + `
    return
  fi
  case $words[CURRENT-1] in
    -c) _files; return ;;
    -C) _files -/; return ;;
    --component|--force) names=(${(f)"$(monorelease __names 2>/dev/null)"}); compadd -a names; return ;;
  esac
  case $words[2] in
    version|manifest|explain|notes) names=(${(f)"$(monorelease __names 2>/dev/null)"}); compadd -a names ;;
    completion) compadd bash zsh fish ;;
  esac
}
if [[ $funcstack[1] == _monorelease ]]; then
  _monorelease "$@"
else
  compdef _monorelease monorelease
fi
`

var fishCompletion = `# fish completion for monorelease. Load it with:  monorelease completion fish | source
complete -c monorelease -f
complete -c monorelease -n '__fish_use_subcommand' -a 'plan' -d 'show what would be released'
complete -c monorelease -n '__fish_use_subcommand' -a 'release' -d 'create tags (and releases with --push)'
complete -c monorelease -n '__fish_use_subcommand' -a 'version' -d 'latest released version'
complete -c monorelease -n '__fish_use_subcommand' -a 'manifest' -d 'umbrella members'
complete -c monorelease -n '__fish_use_subcommand' -a 'changed' -d 'components with changes'
complete -c monorelease -n '__fish_use_subcommand' -a 'explain' -d 'why a component is or is not released'
complete -c monorelease -n '__fish_use_subcommand' -a 'notes' -d 'release notes'
complete -c monorelease -n '__fish_use_subcommand' -a 'completion' -d 'print a shell completion script'
complete -c monorelease -n '__fish_seen_subcommand_from version manifest explain notes' -a '(monorelease __names 2>/dev/null)'
complete -c monorelease -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish'
complete -c monorelease -s c -r -d 'config path'
complete -c monorelease -s C -r -d 'repository directory'
` + fishFlags() + `
`

func fishFlags() string {
	desc := map[string]string{
		"branch": "branch to work from", "ref": "read this ref instead of the checked-out one", "json": "JSON output",
		"stable": "only stable versions", "component": "manifest: one component", "push": "push the tags",
		"remote": "remote name", "force": "force a release", "base": "changed: base ref",
		"with-dependents": "changed: include dependents", "write": "notes: write files", "all": "notes: whole changelog",
		"no-notes": "release: skip notes files", "no-releases": "release: skip GitHub/GitLab releases",
	}
	needsArg := map[string]bool{"branch": true, "ref": true, "component": true, "remote": true, "force": true, "base": true}
	var lines []string
	for _, f := range strings.Fields(flagList) {
		if !strings.HasPrefix(f, "--") {
			continue
		}
		name := strings.TrimPrefix(f, "--")
		arg := ""
		if needsArg[name] {
			arg = " -r"
		}
		lines = append(lines, fmt.Sprintf("complete -c monorelease -l %s%s -d '%s'", name, arg, desc[name]))
	}
	return strings.Join(lines, "\n")
}
