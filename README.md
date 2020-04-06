# Backends
Generic repo for different backend work

## Unit Tests

You need a postgres database:
```
docker run --rm --name some-postgres -p 5432:5432 -e POSTGRES_PASSWORD=docker -d postgres
```

Then use standard go commands, like

```
POSTGRES="host=localhost port=5432 user=postgres password=docker dbname=postgres sslmode=disable" go test ./... -count 1
```

The -count 1 parameter disables test result caching. If you also specify -v you will see t.Log(...) output also for the 
passing unit tests. This can be handy for test-fist development.