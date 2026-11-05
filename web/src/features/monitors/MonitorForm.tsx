import { useEffect, useMemo, useState } from 'react';
import { zodResolver } from '@hookform/resolvers/zod';
import {
	Controller,
	useForm,
	useWatch,
	type UseFormReturn,
} from 'react-hook-form';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from '@/components/ui/select';
import { Textarea } from '@/components/ui/textarea';
import type { ApiProblem } from '@/lib/api/problems';
import type { Monitor } from '@/lib/api/types';
import { applyProblemToForm } from '@/lib/forms';
import { HeadersField } from './HeadersField';
import {
	MONITOR_METHODS,
	methodAllowsBody,
	type SecretEditState,
} from './form';
import { monitorFormSchema, type MonitorFormValues } from './schemas';

const FIELD_MAP: Record<string, keyof MonitorFormValues> = {
	name: 'name',
	url: 'url',
	method: 'method',
	headers: 'headers',
	body: 'body',
	content_type: 'content_type',
	expected_status: 'expected_status',
	interval_seconds: 'interval_seconds',
	timeout_seconds: 'timeout_seconds',
	confirmation_threshold: 'confirmation_threshold',
	recovery_threshold: 'recovery_threshold',
};

export interface MonitorFormProps {
	mode: 'create' | 'edit';
	initialValues: MonitorFormValues;
	monitor?: Monitor;
	submitLabel: string;
	submitting: boolean;
	problem?: ApiProblem | null;
	onSubmit: (
		values: MonitorFormValues,
		secrets: SecretEditState,
	) => Promise<void>;
}

function FieldError({ id, message }: { id: string; message?: string }) {
	if (!message) return null;
	return (
		<p id={id} className="text-sm text-destructive">
			{message}
		</p>
	);
}

function BodyInputs({ form }: { form: UseFormReturn<MonitorFormValues> }) {
	const errors = form.formState.errors;

	return (
		<div className="space-y-3">
			<div className="space-y-1.5">
				<Label htmlFor="monitor-body">Body</Label>
				<Textarea
					id="monitor-body"
					autoComplete="off"
					aria-invalid={errors.body ? true : undefined}
					aria-describedby={errors.body ? 'monitor-body-error' : undefined}
					{...form.register('body')}
				/>
				<FieldError id="monitor-body-error" message={errors.body?.message} />
			</div>
			<div className="space-y-1.5">
				<Label htmlFor="monitor-content-type">Content type</Label>
				<Input
					id="monitor-content-type"
					autoComplete="off"
					placeholder="application/json"
					aria-invalid={errors.content_type ? true : undefined}
					aria-describedby={
						errors.content_type ? 'monitor-content-type-error' : undefined
					}
					{...form.register('content_type')}
				/>
				<FieldError
					id="monitor-content-type-error"
					message={errors.content_type?.message}
				/>
				<p className="text-xs text-muted-foreground">
					Only allowed when a body is present.
				</p>
			</div>
		</div>
	);
}

export function MonitorForm({
	mode,
	initialValues,
	monitor,
	submitLabel,
	submitting,
	problem,
	onSubmit,
}: MonitorFormProps) {
	const [replaceHeaders, setReplaceHeaders] = useState(false);
	const [bodyMode, setBodyMode] = useState<SecretEditState['bodyMode']>('keep');
	const form = useForm<MonitorFormValues>({
		resolver: zodResolver(monitorFormSchema),
		defaultValues: initialValues,
	});

	const errors = form.formState.errors;
	const method = useWatch({ control: form.control, name: 'method' });
	const showBody = methodAllowsBody(method);

	const unmappedMessages = useMemo(() => {
		const fields = problem?.fields;
		if (!fields) return [];
		return Object.entries(fields)
			.filter(([field]) => !(field in FIELD_MAP))
			.map(([, message]) => message);
	}, [problem]);

	useEffect(() => {
		if (!problem) {
			form.clearErrors();
			return;
		}
		applyProblemToForm(problem, form, FIELD_MAP);
	}, [problem, form]);

	const hasMappedFields = Object.keys(problem?.fields ?? {}).length > 0;
	const alertProblem =
		problem && (!hasMappedFields || unmappedMessages.length > 0)
			? {
					...problem,
					detail: [problem.detail, ...unmappedMessages]
						.filter(Boolean)
						.join(' '),
				}
			: null;

	const headerErrors = errors.headers as
		{ message?: string; root?: { message?: string } } | undefined;
	const headersError = errors.headers
		? (headerErrors?.root?.message ??
			headerErrors?.message ??
			'Check each header name and value.')
		: undefined;

	const handleSubmit = form.handleSubmit(async (values) => {
		await onSubmit(values, { replaceHeaders, bodyMode });
	});

	const pending = submitting || form.formState.isSubmitting;

	return (
		<form className="space-y-6" onSubmit={handleSubmit} noValidate>
			<div className="grid gap-4 sm:grid-cols-2">
				<div className="space-y-1.5">
					<Label htmlFor="monitor-name">Name</Label>
					<Input
						id="monitor-name"
						autoComplete="off"
						aria-invalid={errors.name ? true : undefined}
						aria-describedby={errors.name ? 'monitor-name-error' : undefined}
						{...form.register('name')}
					/>
					<FieldError id="monitor-name-error" message={errors.name?.message} />
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="monitor-method">Method</Label>
					<Controller
						control={form.control}
						name="method"
						render={({ field }) => (
							<Select value={field.value} onValueChange={field.onChange}>
								<SelectTrigger
									id="monitor-method"
									className="w-full"
									aria-label="Method"
									aria-invalid={errors.method ? true : undefined}
								>
									<SelectValue placeholder="Select a method" />
								</SelectTrigger>
								<SelectContent>
									{MONITOR_METHODS.map((option) => (
										<SelectItem key={option} value={option}>
											{option}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						)}
					/>
					<FieldError
						id="monitor-method-error"
						message={errors.method?.message}
					/>
				</div>

				<div className="space-y-1.5 sm:col-span-2">
					<Label htmlFor="monitor-url">URL</Label>
					<Input
						id="monitor-url"
						type="url"
						autoComplete="off"
						placeholder="https://api.example.com/health"
						aria-invalid={errors.url ? true : undefined}
						aria-describedby={errors.url ? 'monitor-url-error' : undefined}
						{...form.register('url')}
					/>
					<FieldError id="monitor-url-error" message={errors.url?.message} />
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="monitor-expected-status">Expected status</Label>
					<Input
						id="monitor-expected-status"
						autoComplete="off"
						placeholder="2xx"
						aria-invalid={errors.expected_status ? true : undefined}
						aria-describedby={
							errors.expected_status
								? 'monitor-expected-status-error'
								: undefined
						}
						{...form.register('expected_status')}
					/>
					<FieldError
						id="monitor-expected-status-error"
						message={errors.expected_status?.message}
					/>
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="monitor-interval">Interval (seconds)</Label>
					<Input
						id="monitor-interval"
						type="number"
						min={60}
						max={86400}
						aria-invalid={errors.interval_seconds ? true : undefined}
						aria-describedby={
							errors.interval_seconds ? 'monitor-interval-error' : undefined
						}
						{...form.register('interval_seconds', { valueAsNumber: true })}
					/>
					<FieldError
						id="monitor-interval-error"
						message={errors.interval_seconds?.message}
					/>
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="monitor-timeout">Timeout (seconds)</Label>
					<Input
						id="monitor-timeout"
						type="number"
						min={1}
						max={30}
						aria-invalid={errors.timeout_seconds ? true : undefined}
						aria-describedby={
							errors.timeout_seconds ? 'monitor-timeout-error' : undefined
						}
						{...form.register('timeout_seconds', { valueAsNumber: true })}
					/>
					<FieldError
						id="monitor-timeout-error"
						message={errors.timeout_seconds?.message}
					/>
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="monitor-confirmation-threshold">
						Confirmation threshold
					</Label>
					<Input
						id="monitor-confirmation-threshold"
						type="number"
						min={1}
						max={5}
						aria-invalid={errors.confirmation_threshold ? true : undefined}
						aria-describedby={
							errors.confirmation_threshold
								? 'monitor-confirmation-threshold-error'
								: undefined
						}
						{...form.register('confirmation_threshold', {
							valueAsNumber: true,
						})}
					/>
					<FieldError
						id="monitor-confirmation-threshold-error"
						message={errors.confirmation_threshold?.message}
					/>
				</div>

				<div className="space-y-1.5">
					<Label htmlFor="monitor-recovery-threshold">Recovery threshold</Label>
					<Input
						id="monitor-recovery-threshold"
						type="number"
						min={1}
						max={5}
						aria-invalid={errors.recovery_threshold ? true : undefined}
						aria-describedby={
							errors.recovery_threshold
								? 'monitor-recovery-threshold-error'
								: undefined
						}
						{...form.register('recovery_threshold', { valueAsNumber: true })}
					/>
					<FieldError
						id="monitor-recovery-threshold-error"
						message={errors.recovery_threshold?.message}
					/>
				</div>
			</div>

			<HeadersField
				form={form}
				mode={mode}
				existingHeaderNames={Object.keys(monitor?.headers ?? {})}
				replaceHeaders={replaceHeaders}
				onReplaceHeadersChange={setReplaceHeaders}
				error={headersError}
			/>

			{showBody ? (
				<section className="space-y-3">
					<h2 className="font-heading text-sm font-semibold">Body</h2>
					{mode === 'edit' ? (
						<>
							<p className="text-sm text-muted-foreground">
								{monitor?.has_body ? 'Body configured' : 'No body configured'}
							</p>
							<fieldset className="space-y-2">
								<legend className="text-sm font-medium">Body handling</legend>
								<div className="flex flex-wrap gap-4">
									<Label>
										<input
											type="radio"
											name="body-mode"
											value="keep"
											checked={bodyMode === 'keep'}
											onChange={() => setBodyMode('keep')}
										/>
										Keep
									</Label>
									<Label>
										<input
											type="radio"
											name="body-mode"
											value="replace"
											checked={bodyMode === 'replace'}
											onChange={() => setBodyMode('replace')}
										/>
										Replace
									</Label>
									<Label>
										<input
											type="radio"
											name="body-mode"
											value="clear"
											checked={bodyMode === 'clear'}
											onChange={() => setBodyMode('clear')}
										/>
										Clear
									</Label>
								</div>
							</fieldset>
							{bodyMode === 'replace' ? <BodyInputs form={form} /> : null}
						</>
					) : (
						<BodyInputs form={form} />
					)}
				</section>
			) : null}

			<ProblemAlert problem={alertProblem} />

			<div className="flex justify-end">
				<Button type="submit" disabled={pending}>
					{submitLabel}
				</Button>
			</div>
		</form>
	);
}
