#!/bin/sh
# Stand-in for ffmpeg: copies the -i input to the last argument.
# FAKE_FFMPEG_FAIL=1 makes it fail instead.
if [ -n "$FAKE_FFMPEG_FAIL" ]; then
  exit 1
fi
src=
prev=
for a in "$@"; do
  if [ "$prev" = "-i" ]; then
    src=$a
  fi
  prev=$a
  last=$a
done
cp "$src" "$last"
