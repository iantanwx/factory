#!/bin/sh
# The fixture capture command: it logs the target to $2 and copies the one
# fixed PNG $1 to LOOP_DESIGN_OUT. It fails as $3 says at the target $4:
# exit makes it exit 4, and text makes it write a file that is not a PNG.
printf '%s\n' "$LOOP_DESIGN_TARGET" >> "$2"
if [ "$LOOP_DESIGN_TARGET" = "$4" ]; then
	case "$3" in
	exit) exit 4 ;;
	text) echo "no picture" > "$LOOP_DESIGN_OUT"; exit 0 ;;
	esac
fi
cp "$1" "$LOOP_DESIGN_OUT"
