# flak

Hostnames go in, output comes out.  You can't explain that.

## What is it

Pipe in a list of hostnames (whitespace-separated), or otherwise provide a file containing a list of hosts.  Every argument after `--` on the command line is executed on each of those hosts via `ssh`.  The output is reported to you, the enteprising person-who-needs-to-run-commands in a variety of ways.  It's not smart, it does the one thing.

## How to do?

```
echo server1 server2 127.0.0.1 friend@other-server | flak uptime
server1| 18:24:01 up  9:02,  1 user,  load average: 0.59, 0.68, 0.97
server2| 11:24PM up 137 days,  7:23, 0 users, load averages: 0.69, 0.54, 0.52
```

