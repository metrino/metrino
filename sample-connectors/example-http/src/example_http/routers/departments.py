from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import JSONResponse
import duckdb
from pathlib import Path
from pydantic import BaseModel
from typing import Annotated

class DepartmentParams(BaseModel):
    department_name: str | None = None
    chief_officer_id: str | None = None

# One department as the API returns it. Declared as the routes' response_model so the OpenAPI spec
# describes the response: Trino's openapi connector makes a table's columns from it, and without it a
# table has only the query parameters.
class Department(BaseModel):
    department_id: str
    department_name: str | None = None
    chief_officer_id: str | None = None

router = APIRouter(
    prefix="/departments",
    tags=["departments"]
)

PATH = Path(__file__).resolve().parents[3] / "data" / "departments.csv"
# Every column as text, like the response models and the query parameters.
TABLE = f"read_csv('{PATH}', all_varchar = true)"

@router.get("/", response_model=list[Department])
def get_departments(q_params: Annotated[DepartmentParams, Query()]):
    try:
        sql = f"SELECT * FROM {TABLE}"
        values = []

        sql += " WHERE " if any(q_params.dict().values()) else ""
        for field, value in q_params.dict().items():
            if value is not None:
                sql += f"{field} = ? AND "
                values.append(value)
        sql = sql.rstrip(" AND ")

        departments = duckdb.execute(sql, values).fetchall()
        if not departments:
            return JSONResponse(status_code=404, content={"message": "Department not found"})

        response = []
        for department in departments:
            response.append(
                {
                    "department_id": department[0],
                    "department_name": department[1],
                    "chief_officer_id": department[2],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})

@router.get("/department_id/{value}", response_model=list[Department])
def get_departments_id(value: str):
    try:
        departments = duckdb.execute(f'''
            SELECT * 
            FROM {TABLE} 
            WHERE department_id = ?
            ''', 
            [value]
        ).fetchall()
        if not departments:
            return JSONResponse(status_code=404, content={"message": "Department not found"})

        response = []
        for department in departments:
            response.append(
                {
                    "department_id": department[0],
                    "department_name": department[1],
                    "chief_officer_id": department[2],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})