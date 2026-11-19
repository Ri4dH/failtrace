# README demo. Re-record from the repository root with: vhs docs/demo.tape
# Needs Docker. The first run builds the environment image; record a second
# run so the GIF shows the cached path.

Output docs/demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1360
Set Height 680
Set Padding 24
Set Theme "Dracula"
Set WindowBar Colorful
Set TypingSpeed 60ms
Set PlaybackSpeed 2

Hide
Type "go build -o bin/failtrace ./cmd/failtrace && export PATH=$PWD/bin:$PATH PS1='$ ' && clear"
Enter
Sleep 4s
Show

Sleep 500ms
Type "failtrace repro --platform linux/arm64 https://github.com/stretchr/testify/actions/runs/39247781971"
Sleep 700ms
Enter
Wait+Screen@4m /Reproduced|Not reproduced|Different failure|Diverged|failtrace:/
Sleep 8s
