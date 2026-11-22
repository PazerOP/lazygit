#!/usr/bin/env bash
# Print how long lazygit takes, from launch, to show a line in one of its panes.
set -euo pipefail

bin=$(realpath "$1")
repo=$(realpath "$2")
runs=${3:-5}

cfg=$(mktemp -d)
printf 'git:\n  autoFetch: false\nupdate:\n  method: never\n' >"$cfg/config.yml"
# The welcome popup of a first run would cover the panes
printf 'startuppopupversion: 5\n' >"$cfg/state.yml"

tmux_env=()
lazygit_args=()
if [ -n "${LGBENCH_TRACE:-}" ]; then
	tmux_env+=(-e "GIT_TRACE2_EVENT=$LGBENCH_TRACE")
fi
if [ -n "${LGBENCH_DEBUG_LOG:-}" ]; then
	tmux_env+=(-e "LAZYGIT_LOG_PATH=$LGBENCH_DEBUG_LOG")
	lazygit_args+=(--debug)
fi

# The tmux server exits with its last session, and a session that starts at
# that moment fails. This session keeps the server up between runs.
keepalive="lgbench-$$-keepalive"
tmux new-session -d -s "$keepalive" sleep 3600
trap 'tmux kill-session -t "$keepalive"' EXIT

for i in $(seq "$runs"); do
	session="lgbench-$$-$i"
	start=$(date +%s%N)
	tmux new-session -d -s "$session" -x 200 -y 50 -c "$repo" "${tmux_env[@]}" \
		"$bin" --use-config-dir "$cfg" "${lazygit_args[@]}"
	while :; do
		if ! tmux has-session -t "$session" 2>/dev/null; then
			echo "run $i: lazygit exited before the line showed" >&2
			exit 1
		fi
		if tmux capture-pane -p -t "$session" | grep -qE "${LGBENCH_PATTERN:-^│ +\* }"; then
			break
		fi
		sleep 0.01
	done
	end=$(date +%s%N)
	echo "run $i: $(((end - start) / 1000000)) ms"
	sleep "${LGBENCH_HOLD:-0}"
	if [ -n "${LGBENCH_SCREEN:-}" ]; then
		tmux capture-pane -p -t "$session" >"$LGBENCH_SCREEN"
	fi
	tmux kill-session -t "$session"
done
