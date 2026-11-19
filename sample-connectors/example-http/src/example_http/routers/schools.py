from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import JSONResponse
import duckdb
from pathlib import Path
from pydantic import BaseModel
from typing import Annotated

class SchoolParams(BaseModel):
    school_name: str | None = None
    school_type: str | None = None
    grade_min: str | None = None
    grade_max: str | None = None
    principal_id: str | None = None
    phone: str | None = None
    address: str | None = None
    website: str | None = None

# One school as the API returns it. Declared as the routes' response_model so the OpenAPI spec
# describes the response: Trino's openapi connector makes a table's columns from it, and without it a
# table has only the query parameters.
class School(BaseModel):
    school_id: str
    school_name: str | None = None
    school_type: str | None = None
    grade_min: str | None = None
    grade_max: str | None = None
    principal_id: str | None = None
    phone: str | None = None
    address: str | None = None
    website: str | None = None

router = APIRouter(
    prefix="/schools",
    tags=["schools"]
)

PATH = Path(__file__).resolve().parents[3] / "data" / "schools.csv"
# Every column as text, like the response models and the query parameters.
TABLE = f"read_csv('{PATH}', all_varchar = true)"

@router.get("/", response_model=list[School])
def get_schools(q_params: Annotated[SchoolParams, Query()]):
    try:
        sql = f"SELECT * FROM {TABLE}"
        values = []

        sql += " WHERE " if any(q_params.dict().values()) else ""
        for field, value in q_params.dict().items():
            if value is not None:
                sql += f"{field} = ? AND "
                values.append(value)
        sql = sql.rstrip(" AND ")

        schools = duckdb.execute(sql, values).fetchall()
        if not schools:
            return JSONResponse(status_code=404, content={"message": "School not found"})

        response = []
        for school in schools:
            response.append(
                {
                    "school_id": school[0],
                    "school_name": school[1],
                    "school_type": school[2],
                    "grade_min": school[3],
                    "grade_max": school[4],
                    "principal_id": school[5],
                    "phone": school[6],
                    "address": school[7],
                    "website": school[8],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})

@router.get("/school_id/{value}", response_model=list[School])
def get_schools_id(value: str):
    try:
        schools = duckdb.execute(f'''
            SELECT * 
            FROM {TABLE} 
            WHERE school_id = ?
            ''', 
            [value]
        ).fetchall()
        if not schools:
            return JSONResponse(status_code=404, content={"message": "School not found"})

        response = []
        for school in schools:
            response.append(
                {
                    "school_id": school[0],
                    "school_name": school[1],
                    "school_type": school[2],
                    "grade_min": school[3],
                    "grade_max": school[4],
                    "principal_id": school[5],
                    "phone": school[6],
                    "address": school[7],
                    "website": school[8],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})