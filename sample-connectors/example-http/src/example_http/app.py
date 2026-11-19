from fastapi import FastAPI
from pydantic import BaseModel

from .routers import departments, schools, staff, students

def create_app():
    app = FastAPI()

    app.include_router(departments.router)
    app.include_router(schools.router)
    app.include_router(staff.router)
    app.include_router(students.router)

    return app



