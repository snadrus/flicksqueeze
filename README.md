# flicksqueeze
Squeeze your movies into AV1 in the background.

usage: 

Install FFMPEG CLI with AV1 support.
   Ubuntu: sudo apt install ffmpeg
   Mac: brew install ffmpeg

```
go install github.com/snadrus/flicksqueeze
./flicksqueeze --no-delete ~
```

(feel free to start it as a background task)
This will persistently convert movies to an efficient AV1 encoding, slowly increasing your storage space. 
