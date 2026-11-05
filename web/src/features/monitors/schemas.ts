import { z } from 'zod';

export const monitorFormSchema = z.object({
	name: z.string().trim().min(1, 'Name is required').max(100),
	url: z
		.string()
		.trim()
		.min(1, 'URL is required')
		.max(2048)
		.url('Enter a valid URL')
		.refine(
			(value) => /^https?:\/\//i.test(value),
			'URL must start with http:// or https://',
		),
	method: z.enum(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE']),
	expected_status: z.string().trim().min(1, 'Expected status is required'),
	interval_seconds: z.number().int().min(60).max(86400),
	timeout_seconds: z.number().int().min(1).max(30),
	confirmation_threshold: z.number().int().min(1).max(5),
	recovery_threshold: z.number().int().min(1).max(5),
	headers: z
		.array(z.object({ name: z.string().min(1), value: z.string() }))
		.max(20),
	body: z.string().max(16 * 1024),
	content_type: z.string().max(200),
});

export type MonitorFormValues = z.infer<typeof monitorFormSchema>;
export type HeaderRow = MonitorFormValues['headers'][number];
