# Anki Stats Exporter

A planned self-hosted service for displaying Anki study statistics in
[Homepage](https://github.com/gethomepage/homepage).

## Status

Project scaffold. The exporter is not implemented yet.

## Intended behavior

- Synchronize a separate collection from a configurable Anki sync server.
- Calculate reviews today, study time, study streak, and retention.
- Serve those statistics as JSON for Homepage's Custom API widget.
- Run without Anki Desktop and avoid opening the sync server's live database.
- Respect the collection's study-day rollover and the configured timezone.

## Proposed data flow

```text
Anki devices -> Anki sync server -> Exporter's separate collection
                                            |
                                      Study statistics
                                            |
                                        JSON API
                                            |
                                 Homepage Custom API widget
```

The API should report its last successful sync time so consumers can distinguish
current statistics from stale data. Sync credentials must remain outside the
repository.

## References

- [Anki self-hosted sync server](https://docs.ankiweb.net/manual/sync-server)
- [Homepage Custom API widget](https://gethomepage.dev/widgets/services/customapi/)
