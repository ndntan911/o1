// Runs at the shipped limits (60/min, burst 20 per IP) and asserts the limiter
// actually returns 429 rather than measuring throughput.
import http from 'k6/http';
import { Counter } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const SLUG = __ENV.SLUG || 'load-01';
const limited = new Counter('rate_limited');

export const options = {
	scenarios: {
		limits: {
			executor: 'constant-arrival-rate',
			rate: 2,
			timeUnit: '1s',
			duration: '30s',
			preAllocatedVUs: 10,
			maxVUs: 20,
		},
	},
	thresholds: {
		rate_limited: ['count>0'],
	},
};

export default function () {
	const res = http.get(`${BASE}/api/v1/public/status/${SLUG}`);
	if (res.status === 429) {
		limited.add(1);
	}
}
