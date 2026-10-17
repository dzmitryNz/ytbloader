#!/bin/sh
# Stand-in for yt-dlp. Behaviour is driven by environment variables:
#   FAKE_ARGV_FILE  where to record the received argv, one argument per line
#   FAKE_OUT_DIR    directory for the fake downloaded file
#   FAKE_SLEEP      seconds to hang after starting (for cancellation tests)
#   FAKE_FAIL_MSG   print this to stderr and exit 1
: > "$FAKE_ARGV_FILE"
for a in "$@"; do
  printf '%s\n' "$a" >> "$FAKE_ARGV_FILE"
done

path_file=
prev2=
prev=
for a in "$@"; do
  if [ "$prev2" = "--print-to-file" ]; then
    path_file=$a
  fi
  prev2=$prev
  prev=$a
done

if [ -n "$FAKE_FAIL_MSG" ]; then
  echo "$FAKE_FAIL_MSG" >&2
  exit 1
fi

echo "[youtube] extracting"
echo "[ytbloader-progress]  10.0%"
echo "[ytbloader-progress]  50.0%"
echo "[ytbloader-progress] 100.0%"

if [ -n "$FAKE_SLEEP" ]; then
  exec sleep "$FAKE_SLEEP"
fi

out="$FAKE_OUT_DIR/song.webm"
printf 'audio' > "$out"
printf '%s\n' "$out" > "$path_file"
