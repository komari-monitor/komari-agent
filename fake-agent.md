# Fake Agent

`fake_agent.py` creates a test-only Komari v2 client that reports generated system metrics. It only advertises and processes `agent.ping`; it does not connect to, advertise, or process terminal or command-execution requests.

Create and persist a new client locally:

```powershell
python .\fake_agent.py --server https://komari.example.com --adkey YOUR_AUTO_DISCOVERY_KEY -new
```

The generated client token is saved in `fake-agent-client.json` alongside the script. Subsequent runs reuse it:

```powershell
python .\fake_agent.py --server https://komari.example.com --adkey unused
```

Use `--state` for a separate saved client, `--name` to set the new client's name suffix, and `--interval` to change the three-second report interval. `--adkey` remains required to make accidental invocation against the wrong server explicit, but it is sent only when `-new` registers a client.
