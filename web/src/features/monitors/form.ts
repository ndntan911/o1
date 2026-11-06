import type {
	CreateMonitorInput,
	HttpMethod,
	Monitor,
	UpdateMonitorInput,
} from '@/lib/api/types';
import type { HeaderRow, MonitorFormValues } from './schemas';

export type SecretEditState = {
	replaceHeaders: boolean;
	bodyMode: 'keep' | 'replace' | 'clear';
};

export const MONITOR_METHODS: HttpMethod[] = [
	'GET',
	'HEAD',
	'POST',
	'PUT',
	'PATCH',
	'DELETE',
];

export function methodAllowsBody(method: HttpMethod): boolean {
	return method !== 'GET' && method !== 'HEAD';
}

export function defaultMonitorValues(): MonitorFormValues {
	return {
		name: '',
		url: '',
		method: 'GET',
		expected_status: '2xx',
		interval_seconds: 60,
		timeout_seconds: 10,
		confirmation_threshold: 2,
		recovery_threshold: 2,
		headers: [],
		body: '',
		content_type: '',
	};
}

export function monitorToFormValues(monitor: Monitor): MonitorFormValues {
	return {
		name: monitor.name,
		url: monitor.url,
		method: monitor.method,
		expected_status: monitor.expected_status,
		interval_seconds: monitor.interval_seconds,
		timeout_seconds: monitor.timeout_seconds,
		confirmation_threshold: monitor.confirmation_threshold,
		recovery_threshold: monitor.recovery_threshold,
		// Header values are masked and bodies are never returned. Never copy
		// either into form state: doing so would round-trip "***" as a secret.
		headers: [],
		body: '',
		content_type: monitor.content_type ?? '',
	};
}

export function headerRowsToRecord(rows: HeaderRow[]): Record<string, string> {
	const headers: Record<string, string> = {};

	for (const row of rows) {
		const name = row.name.trim();
		if (name === '') continue;
		headers[name] = row.value;
	}

	return headers;
}

export function buildCreatePayload(
	values: MonitorFormValues,
): CreateMonitorInput {
	const payload: CreateMonitorInput = {
		name: values.name,
		url: values.url,
		method: values.method,
		headers: headerRowsToRecord(values.headers),
		expected_status: values.expected_status,
		interval_seconds: values.interval_seconds,
		timeout_seconds: values.timeout_seconds,
		confirmation_threshold: values.confirmation_threshold,
		recovery_threshold: values.recovery_threshold,
	};

	if (methodAllowsBody(values.method)) {
		if (values.body !== '') payload.body = values.body;
		if (values.body !== '' && values.content_type !== '') {
			payload.content_type = values.content_type;
		}
	}

	return payload;
}

const PLAIN_FIELDS = [
	'name',
	'url',
	'method',
	'expected_status',
	'interval_seconds',
	'timeout_seconds',
	'confirmation_threshold',
	'recovery_threshold',
] as const;

export function buildUpdatePayload(
	values: MonitorFormValues,
	_initial: MonitorFormValues,
	dirty: Partial<Record<keyof MonitorFormValues, boolean>>,
	secrets: SecretEditState,
): UpdateMonitorInput {
	const payload: UpdateMonitorInput = {};

	for (const field of PLAIN_FIELDS) {
		if (dirty[field]) {
			Object.assign(payload, { [field]: values[field] });
		}
	}

	if (secrets.replaceHeaders) {
		payload.headers = headerRowsToRecord(values.headers);
	}

	if (secrets.bodyMode === 'clear') {
		payload.body = '';
	} else if (
		methodAllowsBody(values.method) &&
		secrets.bodyMode === 'replace'
	) {
		payload.body = values.body;
		if (values.body !== '') {
			// An empty content type clears the stored value while keeping a body.
			payload.content_type = values.content_type;
		}
	}

	return payload;
}
