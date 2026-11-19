# Example HTTP Server

## Usage

```bash
uv run uvicorn example_http:app --reload 
```

## Endpoints

| Method | Endpoint                  | Desc                                               |
|--------|---------------------------|----------------------------------------------------|
| GET    | /{table}                  | Show all rows in table                             |
| GET    | /{table}/{column}/{value} | Show all rows where the value exists in the column |

## Dependencies

FastAPI 
DuckDB 
Uvicorn
Pydantic

## OpenAPI Specification

Trino's openapi connector reads the spec FastAPI generates at `/openapi.json` (`spec-location` in the catalog) when Trino starts, so restart Trino after changing an endpoint. Each endpoint becomes a table (`/staff/` is `staff_`, `/staff/staff_id/{value}` is `staff_staff_id`). A table's columns are its endpoint's parameters plus the fields of its response, so every route must declare a `response_model`: without one the spec doesn't describe the response, and the table has only the parameters. Required parameters, such as `value`, must be filtered on in every query.

Specifications on how to generate new endpoints and how to interpret paths can be found on the OpenAPI [doc](https://swagger.io/docs/specification/v3_0/about/) 

