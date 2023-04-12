# abc

A simple yet fast video backup tool for abc.net.au

## Build/install

Use Go to compile the program.

`go install .`

## Download a video

Using the URL of the content you want to backup:

`$ abc -url https://iview.abc.net.au/video/FA1905H001S00`

The content gets downlaoded and converted to a mp4 file with subtitles if available.
