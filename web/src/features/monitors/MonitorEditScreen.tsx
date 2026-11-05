import { useMemo, useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import type { Workspace } from '@/lib/api/types';
import {
	buildUpdatePayload,
	monitorToFormValues,
	type SecretEditState,
} from './form';
import { useMonitor, useUpdateMonitor } from './hooks';
import { MonitorForm } from './MonitorForm';
import type { MonitorFormValues } from './schemas';

function unexpectedProblem(): ApiProblem {
	return {
		type: 'about:blank',
		title: 'Something went wrong',
		status: 0,
		code: 'internal_error',
		detail: 'Please try again.',
	};
}

function monitorDetailHref(workspaceSlug: string, monitorId: string): string {
	return `/w/${encodeURIComponent(workspaceSlug)}/monitors/${encodeURIComponent(monitorId)}`;
}

function dirtyFields(
	values: MonitorFormValues,
	initial: MonitorFormValues,
): Partial<Record<keyof MonitorFormValues, boolean>> {
	const dirty: Partial<Record<keyof MonitorFormValues, boolean>> = {};
	const fields = [
		'name',
		'url',
		'method',
		'expected_status',
		'interval_seconds',
		'timeout_seconds',
		'confirmation_threshold',
		'recovery_threshold',
	] as const;

	for (const field of fields) {
		if (values[field] !== initial[field]) dirty[field] = true;
	}

	return dirty;
}

export interface MonitorEditScreenProps {
	workspace: Workspace;
	monitorId: string;
}

export function MonitorEditScreen({
	workspace,
	monitorId,
}: MonitorEditScreenProps) {
	const navigate = useNavigate();
	const monitorQuery = useMonitor(workspace.id, monitorId);
	const mutation = useUpdateMonitor(workspace.id, monitorId);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	const initialValues = useMemo(
		() => (monitorQuery.data ? monitorToFormValues(monitorQuery.data) : null),
		[monitorQuery.data],
	);

	if (monitorQuery.isPending) return <PageSkeleton />;

	if (monitorQuery.isError || !monitorQuery.data || !initialValues) {
		const notFound =
			isApiProblem(monitorQuery.error) &&
			monitorQuery.error.problem.code === 'not_found';
		return (
			<ErrorPanel
				title={notFound ? 'Monitor not found' : 'Could not load monitor'}
				message={
					notFound
						? 'This monitor may have been deleted.'
						: isApiProblem(monitorQuery.error)
							? (monitorQuery.error.problem.detail ??
								monitorQuery.error.problem.title)
							: 'Please try again.'
				}
				onRetry={notFound ? undefined : () => void monitorQuery.refetch()}
			/>
		);
	}

	const initial = initialValues;

	async function handleSubmit(
		values: MonitorFormValues,
		secrets: SecretEditState,
	) {
		setProblem(null);
		try {
			await mutation.mutateAsync(
				buildUpdatePayload(
					values,
					initial,
					dirtyFields(values, initial),
					secrets,
				),
			);
			await navigate({
				href: monitorDetailHref(workspace.slug, monitorId),
			});
		} catch (error) {
			setProblem(isApiProblem(error) ? error.problem : unexpectedProblem());
		}
	}

	return (
		<section className="space-y-6">
			<div className="space-y-1">
				<h1 className="font-heading text-xl font-semibold">Edit monitor</h1>
				<p className="text-sm text-muted-foreground">
					Update checks for {monitorQuery.data.name}.
				</p>
			</div>

			<MonitorForm
				mode="edit"
				initialValues={initial}
				monitor={monitorQuery.data}
				submitLabel="Save changes"
				submitting={mutation.isPending}
				problem={problem}
				onSubmit={handleSubmit}
			/>

			<Link
				to="/w/$workspaceSlug/monitors"
				params={{ workspaceSlug: workspace.slug }}
				className="text-sm text-muted-foreground underline underline-offset-4"
			>
				Back to monitors
			</Link>
		</section>
	);
}
