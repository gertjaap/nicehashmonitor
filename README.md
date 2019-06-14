# Nicehash Monitor

This software connects as a miner to the Stratum servers of Nicehash, and logs all 
the blockhashes a miner is asked to mine on. Theoretically, this would allow detecting
someone using Nicehash for mining private blocks - if there are blockhashes logged
by this monitor that don't exist on any public known chain of that algorithm - it's 
possible someone is withholding blocks.

## Running

```
git clone https://github.com/gertjaap/nicehashmonitor
cd nicehashmonitor
go get ./...
go build
./nicehashmonitor
```

Data will be stored in `/data` (sorry this is hardcoded for now).

The software, while running, exposes a simple REST based JSON API:

`http://{your ip}:8915/list/{start}/{end}` will show the most recently retrieved work for all algorithms 
`http://{your ip}:8915/listByHash/{hash}` will show work retrieved matching the given hash
`http://{your ip}:8915/listByServer/{server prefix}/{start}/{end}` will show work for a specific server most recently retrieved. You could for example use `sha256` as prefix

