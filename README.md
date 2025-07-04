# kvv

TUI Key Vault Viewer for Azure Key Vault

# Running

## Running from Taskfile (also builds):

```
task run -- <your-key-vault-URI>
```

## Plain Running

```
task build
build/kvv <your-key-vault-URI>
```

# Persistent configuration

## Config File

If you want a peristent list of multiple Key Vaults, and/or prefer not to have to type a URI
on the command line, create a file like the following at `~/.kvv`

```
vaults:
  - "https://vault-001.vault.azure.net"
  - "https://vault-002.vault.azure.net"
```

These will appear in the `Vault:` dropdown.

## Running with a config file

With a config file in place, all you have to do is:

```
task run
```
