from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import JSONResponse
import duckdb
from pathlib import Path
from pydantic import BaseModel
from typing import Annotated

class StudentParams(BaseModel):
    first_name: str | None = None
    last_name: str | None = None
    school_id: str | None = None
    date_enrolled: str | None = None
    expected_graduation: str | None = None
    email: str | None = None

# One student as the API returns it. Declared as the routes' response_model so the OpenAPI spec
# describes the response: Trino's openapi connector makes a table's columns from it, and without it a
# table has only the query parameters.
class Student(BaseModel):
    student_id: str
    first_name: str | None = None
    last_name: str | None = None
    school_id: str | None = None
    date_enrolled: str | None = None
    expected_graduation: str | None = None
    email: str | None = None

router = APIRouter(
    prefix="/students",
    tags=["students"]
)

PATH = Path(__file__).resolve().parents[3] / "data" / "students.csv"
# Every column as text, like the response models and the query parameters.
TABLE = f"read_csv('{PATH}', all_varchar = true)"

@router.get("/", response_model=list[Student])
def get_students(q_params: Annotated[StudentParams, Query()]):
    try:
        sql = f"SELECT * FROM {TABLE}"
        values = []

        sql += " WHERE " if any(q_params.dict().values()) else ""
        for field, value in q_params.dict().items():
            if value is not None:
                sql += f"{field} = ? AND "
                values.append(value)
        sql = sql.rstrip(" AND ")

        students = duckdb.execute(sql, values).fetchall()
        if not students:
            return JSONResponse(status_code=404, content={"message": "Student not found"})
        
        response = []
        for student in students:
            response.append(
                {
                    "student_id": student[0],
                    "first_name": student[1],
                    "last_name": student[2],
                    "school_id": student[3],
                    "date_enrolled": student[4],
                    "expected_graduation": student[5],
                    "email": student[6],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})

@router.get("/student_id/{value}", response_model=list[Student])
def get_students_id(value: str):
    try:
        students = duckdb.execute(f'''
            SELECT * 
            FROM {TABLE} 
            WHERE student_id = ?
            ''', 
            [value]
        ).fetchall()
        if not students:
            return JSONResponse(status_code=404, content={"message": "Student not found"})
        
        response = []
        for student in students:
            response.append(
                {
                    "student_id": student[0],
                    "first_name": student[1],
                    "last_name": student[2],
                    "school_id": student[3],
                    "date_enrolled": student[4],
                    "expected_graduation": student[5],
                    "email": student[6],
                }
            )
        return response
    except Exception as e:
        return JSONResponse(status_code=400, content={"message": "Unexpected Error"})