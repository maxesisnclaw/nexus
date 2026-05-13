# E2E test client. Build context: repo root.
FROM python:3.12-slim
RUN pip install --no-cache-dir requests "msgpack>=1.0" "nexus-rpc-sdk>=0.5.2"
WORKDIR /app
COPY examples/microservices-compose/client/verify.py /app/verify.py
ENTRYPOINT ["python", "-u", "/app/verify.py"]
