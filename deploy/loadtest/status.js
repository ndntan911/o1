// Public status page reads against a stack whose public limiter is raised.
// Thresholds: p95 < 300 ms, p99 < 600 ms, failures < 0.1% at the default rate.
import http from 'k6/http';
import { check } from 'k6';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const SLUG = __ENV.SLUG || 'load-01';

export const options = {
	scenarios: {
		status: {
			executor: 'constant-arrival-rate',
			rate: Number(__ENV.RATE || 200),
			timeUnit: '1s',
			duration: __ENV.DURATION || '5m',
			preAllocatedVUs: 50,
			maxVUs: 300,
		},
	},
	thresholds: {
		http_req_failed: ['rate<0.001'],
		http_req_duration: ['p(95)<300', 'p(99)<600'],
	},
};

export default function () {
	const res = http.get(`${BASE}/api/v1/public/status/${SLUG}`, {
		tags: { name: 'public-status' },
	});
	check(res, { 'status 200': (r) => r.status === 200 });
}
