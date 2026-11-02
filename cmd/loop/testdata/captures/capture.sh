#!/bin/sh
# The fixture capture command: it logs the target to $1 and copies the
# checkout's fixed.png, by a path relative to the working directory, to
# LOOP_DESIGN_OUT. So it captures only when it runs in the checkout. It fails
# as $2 says at the target $3: exit makes it write the PNG and then exit 4,
# text makes it write a file that is not a PNG, and none makes it write no
# file.
printf '%s\n' "$LOOP_DESIGN_TARGET" >> "$1"
if [ "$LOOP_DESIGN_TARGET" = "$3" ]; then
	case "$2" in
	exit) cp fixed.png "$LOOP_DESIGN_OUT"; exit 4 ;;
	text) echo "no picture" > "$LOOP_DESIGN_OUT"; exit 0 ;;
	none) exit 0 ;;
	esac
fi
cp fixed.png "$LOOP_DESIGN_OUT"
