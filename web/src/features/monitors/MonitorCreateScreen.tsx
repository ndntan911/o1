import { useState } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { isApiProblem } from '@/lib/api/client';
import type { ApiProblem } from '@/lib/api/problems';
import type { Monitor, Workspace } from '@/lib/api/types';
import { buildCreatePayload, defaultMonitorValues } from './form';
import { useCreateMonitor } from './hooks';
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

export interface MonitorCreateScreenProps {
	workspace: Workspace;
}

export function MonitorCreateScreen({ workspace }: MonitorCreateScreenProps) {
	const navigate = useNavigate();
	const mutation = useCreateMonitor(workspace.id);
	const [problem, setProblem] = useState<ApiProblem | null>(null);

	async function handleSubmit(values: MonitorFormValues) {
		setProblem(null);
		try {
			const monitor: Monitor = await mutation.mutateAsync(
				buildCreatePayload(values),
			);
			await navigate({ href: monitorDetailHref(workspace.slug, monitor.id) });
		} catch (error) {
			setProblem(isApiProblem(error) ? error.problem : unexpectedProblem());
		}
	}

	return (
		<section className="space-y-6">
			<div className="space-y-1">
				<h1 className="font-heading text-xl font-semibold">New monitor</h1>
				<p className="text-sm text-muted-foreground">
					Choose an HTTP endpoint, how often to check it, and what response
					counts as healthy.
				</p>
			</div>

			<MonitorForm
				mode="create"
				initialValues={defaultMonitorValues()}
				submitLabel="Create monitor"
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
