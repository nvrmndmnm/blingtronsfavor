# bash completion for bfctl — install as /etc/bash_completion.d/bfctl
# or source it from ~/.bashrc.
_bfctl() {
	local cur prev cmds
	COMPREPLY=()
	cur="${COMP_WORDS[COMP_CWORD]}"
	prev="${COMP_WORDS[COMP_CWORD-1]}"
	cmds="init adopt add sync status remove tui version"

	case "$prev" in
	--flavor)
		COMPREPLY=( $(compgen -W "mainline forever classic mists" -- "$cur") )
		return 0
		;;
	--strategy)
		COMPREPLY=( $(compgen -W "release" -- "$cur") )
		return 0
		;;
	--path|--scan|--branch|--interface)
		COMPREPLY=( $(compgen -f -- "$cur") )
		return 0
		;;
	esac

	if [ "$COMP_CWORD" -eq 1 ]; then
		COMPREPLY=( $(compgen -W "$cmds" -- "$cur") )
		return 0
	fi

	case "${COMP_WORDS[1]}" in
	sync)
		COMPREPLY=( $(compgen -W "--check-only --explain --json --verbose --quiet" -- "$cur") )
		;;
	status)
		COMPREPLY=( $(compgen -W "--json --offline --scan --flavor --interface --verbose --quiet" -- "$cur") )
		;;
	add)
		COMPREPLY=( $(compgen -W "--branch --strategy --flavor --force --explain --verbose --quiet" -- "$cur") )
		;;
	remove)
		COMPREPLY=( $(compgen -W "--flavor --verbose --quiet" -- "$cur") )
		;;
	init)
		COMPREPLY=( $(compgen -W "--path --verbose --quiet" -- "$cur") )
		;;
	*)
		COMPREPLY=( $(compgen -W "--verbose --quiet" -- "$cur") )
		;;
	esac
}
complete -F _bfctl bfctl
