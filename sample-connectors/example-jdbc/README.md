# JDBC Sample Data Source

## Usage

```bash
docker compose up -d --build
```

## To run

To open the Postgres CLI for direct SQL querying:
```bash
docker exec -it my_postgres_db psql -h localhost -U postgres sampledb 
```