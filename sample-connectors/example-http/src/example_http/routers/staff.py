from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import JSONResponse
import duckdb
from pathlib import Path
from pydantic import BaseModel
from typing import Annotated

class StaffParams(BaseModel):
    first_name: str | None = None
    last_name: str | None = None
    school_id: str | None = None
    email: str | None = None
    phone: str | None = None
    department_id: str | None = None

# One staff member as the API returns it. Declared as the routes' response_model so the OpenAPI spec
# describes the response: Trino's openapi connector makes a table's columns from it, and without it a
# table has only the query parameters.
class Staff(BaseModel):
    staff_id: str
    first_name: str | None = None
    last_name: str | None = None
    school_id: str | None = None
    email: str | None = None
    phone: str | None = None
    department_id: str | None = None

router = APIRouter(
    prefix="/staff",
    tags=["staff"]
)

PATH = Path(__file__).resolve().parents[3] / "data" / "staff.csv"
# Every column as text, like the response models and the query parameters.
TABLE = f"read_csv('{PATH}', all_varchar = true)"

@router.get("/", response_model=list[Staff])
def get_staff(q_params: Annotated[StaffParams, Query()]):
    try:
        sql = f"SELECT * FROM {TABLE}"
        values = []

        sql += " WHERE " if any(q_params.dict().values()) else ""
        for field, value in q_params.dict().items():
            if value is not None:
                sql += f"{field} = ? AND "
                values.append(value)
        sql = sql.rstrip(" AND ")

        staff = duckdb.execute(sql, values).fetchall()
        if not staff:
            return JSONResponse(status_code=404, content={"message": "Staff not found"})

        response = []
        for _staff in staff:
            response.append(
                {
                    "staff_id": _staff[0],
                    "first_name": _staff[1],
                    "last_name": _staff[2],
                    "school_id": _staff[3],
                    "email": _staff[4],
                    "phone": _staff[5],
                    "department_id": _staff[6],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})

@router.get("/staff_id/{value}", response_model=list[Staff])
def get_staff_id(value: str):
    try:
        staff = duckdb.execute(f'''
            SELECT * 
            FROM {TABLE} 
            WHERE staff_id = ?
            ''', 
            [value]
        ).fetchall()
        if not staff:
            return JSONResponse(status_code=404, content={"message": "Staff not found"})

        response = []
        for _staff in staff:
            response.append(
                {
                    "staff_id": _staff[0],
                    "first_name": _staff[1],
                    "last_name": _staff[2],
                    "school_id": _staff[3],
                    "email": _staff[4],
                    "phone": _staff[5],
                    "department_id": _staff[6],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})