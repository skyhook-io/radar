"""Local fixture: OpenAI chat completions and one read-only MCP tool."""
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Handler(BaseHTTPRequestHandler):
    def send_json(self, value):
        data = json.dumps(value).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.send_json({'object': 'list', 'data': [{'id': 'stub-model', 'object': 'model'}]})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        if self.path == '/mcp':
            if 'id' not in body:
                self.send_response(202)
                self.end_headers()
                return
            method = body.get('method')
            if method == 'initialize':
                result = {'protocolVersion': '2024-11-05', 'capabilities': {'tools': {}},
                          'serverInfo': {'name': 'radar-kagent-demo', 'version': '1.0'}}
            elif method == 'tools/list':
                result = {'tools': [{'name': 'demo_echo', 'description': 'Echo a fixture string',
                                     'inputSchema': {'type': 'object', 'properties': {'text': {'type': 'string'}}}}]}
            elif method == 'tools/call':
                result = {'content': [{'type': 'text', 'text': 'demo echo'}]}
            else:
                result = {}
            self.send_json({'jsonrpc': '2.0', 'id': body['id'], 'result': result})
            return
        base = {'id': 'chatcmpl-demo', 'created': int(time.time()), 'model': 'stub-model'}
        if not body.get('stream'):
            self.send_json({**base, 'object': 'chat.completion', 'choices': [
                {'index': 0, 'message': {'role': 'assistant', 'content': 'radar demo ok'}, 'finish_reason': 'stop'}],
                'usage': {'prompt_tokens': 1, 'completion_tokens': 3, 'total_tokens': 4}})
            return
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.send_header('Connection', 'close')
        self.end_headers()
        for delta, finish in [({'role': 'assistant', 'content': 'radar demo ok'}, None), ({}, 'stop')]:
            chunk = {**base, 'object': 'chat.completion.chunk', 'choices': [
                {'index': 0, 'delta': delta, 'finish_reason': finish}]}
            self.wfile.write(('data: ' + json.dumps(chunk) + '\n\n').encode())
        self.wfile.write(b'data: [DONE]\n\n')
        self.wfile.flush()
        self.close_connection = True


ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
