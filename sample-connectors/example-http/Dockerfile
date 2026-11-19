FROM ghcr.io/astral-sh/uv:python3.12-trixie-slim

WORKDIR /example-http

COPY . .

RUN uv sync --locked

EXPOSE 8000

CMD ["uv", "run", "uvicorn", "example_http:app", "--host", "0.0.0.0", "--port", "8000"]