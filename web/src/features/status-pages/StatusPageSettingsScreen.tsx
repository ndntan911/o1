import { useState } from 'react';
import { toast } from 'sonner';
import { ErrorPanel } from '@/components/feedback/ErrorPanel';
import { PageSkeleton } from '@/components/feedback/Skeletons';
import { ProblemAlert } from '@/components/feedback/ProblemAlert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import { useMonitors } from '@/features/monitors/hooks';
import { isApiProblem } from '@/lib/api/client';
import { problemFromError } from '@/lib/api/problem-from-error';
import type { ApiProblem } from '@/lib/api/problems';
import type {
	Monitor,
	MonitorStatus,
	StatusPageConfig,
	Workspace,
} from '@/lib/api/types';
import { canManageStatusPage } from '@/lib/roles';
import { useStatusPage, useUpdateStatusPage } from './hooks';

const DESCRIPTION_MAX = 500;

const MONITOR_STATUS_LABELS: Record<MonitorStatus, string> = {
	up: 'Up',
	down: 'Down',
	unknown: 'Unknown',
	paused: 'Paused',
};

function errorMessage(error: unknown): string {
	if (isApiProblem(error)) {
		return error.problem.detail ?? error.problem.title;
	}
	if (error instanceof Error) return error.message;
	return 'Please try again.';
}

export interface StatusPageSettingsScreenProps {
	workspace: Workspace;
}

export function StatusPageSettingsScreen({
	workspace,
}: StatusPageSettingsScreenProps) {
	if (!canManageStatusPage(workspace.role)) {
		return (
			<section className="space-y-2">
				<h1 className="font-heading text-xl font-semibold">Status page</h1>
				<p className="text-sm text-muted-foreground">
					You do not have access to status page settings.
				</p>
			</section>
		);
	}

	return <StatusPageSettingsContent workspace={workspace} />;
}

function StatusPageSettingsContent({
	workspace,
}: StatusPageSettingsScreenProps) {
	const configQuery = useStatusPage(workspace.id);
	const monitorsQuery = useMonitors(workspace.id);

	if (configQuery.isPending || monitorsQuery.isPending) {
		return <PageSkeleton />;
	}

	if (!configQuery.data) {
		return (
			<ErrorPanel
				title="Could not load status page settings"
				message={errorMessage(configQuery.error)}
				problem={problemFromError(configQuery.error)}
				onRetry={() => void configQuery.refetch()}
			/>
		);
	}

	if (!monitorsQuery.data) {
		return (
			<ErrorPanel
				title="Could not load monitors"
				message={errorMessage(monitorsQuery.error)}
				problem={problemFromError(monitorsQuery.error)}
				onRetry={() => void monitorsQuery.refetch()}
			/>
		);
	}

	const monitors = monitorsQuery.data.pages.flatMap((page) => page.monitors);

	return (
		<StatusPageSettingsForm
			workspace={workspace}
			config={configQuery.data}
			monitors={monitors}
		/>
	);
}

interface StatusPageSettingsFormProps {
	workspace: Workspace;
	config: StatusPageConfig;
	monitors: Monitor[];
}

function StatusPageSettingsForm({
	workspace,
	config,
	monitors,
}: StatusPageSettingsFormProps) {
	const update = useUpdateStatusPage(workspace.id);
	// The form mounts once the config query has resolved, so these state
	// initializers only run for the first load. Background refetches pass new
	// props but must not overwrite edits the user is still working on.
	const [enabled, setEnabled] = useState(config.enabled);
	const [title, setTitle] = useState(config.title || workspace.name);
	const [description, setDescription] = useState(config.description);
	const [selectedIds, setSelectedIds] = useState<Set<string>>(
		() => new Set(config.monitor_ids),
	);
	const [validation, setValidation] = useState<string | null>(null);
	const [problem, setProblem] = useState<ApiProblem | null>(null);
	const [copied, setCopied] = useState(false);

	const publicUrl = `${window.location.origin}/status/${workspace.slug}`;
	const fields = problem?.fields ?? {};
	const titleError = fields.title;
	const descriptionError = fields.description;
	const monitorsError = fields.monitor_ids ?? fields.monitors;

	function toggleMonitor(monitorId: string, checked: boolean) {
		setSelectedIds((current) => {
			const next = new Set(current);
			if (checked) next.add(monitorId);
			else next.delete(monitorId);
			return next;
		});
		setValidation(null);
	}

	function handleSave() {
		setProblem(null);
		const trimmedTitle = title.trim();
		const trimmedDescription = description.trim();

		if (!trimmedTitle) {
			setValidation('Title is required.');
			return;
		}

		const monitorIds = monitors
			.filter((monitor) => selectedIds.has(monitor.id))
			.map((monitor) => monitor.id);

		if (enabled && monitorIds.length === 0) {
			setValidation('Select at least one monitor before enabling the page.');
			return;
		}

		setValidation(null);
		update.mutate(
			{
				enabled,
				title: trimmedTitle,
				description: trimmedDescription,
				monitor_ids: monitorIds,
			},
			{
				onError: (error) => setProblem(problemFromError(error)),
				onSuccess: () => {
					setProblem(null);
					toast.success('Status page saved');
				},
			},
		);
	}

	async function handleCopy() {
		setCopied(false);
		try {
			await navigator.clipboard.writeText(publicUrl);
			setCopied(true);
			toast.success('Public link copied');
		} catch {
			setCopied(false);
			toast.error('Could not copy the link');
		}
	}

	return (
		<section className="max-w-2xl space-y-6">
			<div className="space-y-1">
				<h1 className="font-heading text-xl font-semibold">Status page</h1>
				<p className="text-sm text-muted-foreground">
					Publish a public page showing the health of {workspace.name}.
				</p>
			</div>

			{problem ? <ProblemAlert problem={problem} /> : null}

			<div className="flex flex-wrap items-center justify-between gap-4 rounded-xl border p-4">
				<div className="space-y-1">
					<Label htmlFor="status-page-enabled">Status page enabled</Label>
					<p className="text-sm text-muted-foreground">
						Make the public status page available at its link.
					</p>
				</div>
				<Switch
					id="status-page-enabled"
					aria-label="Status page enabled"
					checked={enabled}
					disabled={update.isPending}
					onCheckedChange={(checked) => {
						setEnabled(checked);
						setValidation(null);
					}}
				/>
			</div>

			{config.enabled ? (
				<div className="space-y-2">
					<Label htmlFor="status-page-public-link">Public link</Label>
					<div className="flex flex-wrap items-center gap-2">
						<Input
							id="status-page-public-link"
							readOnly
							value={publicUrl}
							onFocus={(event) => event.currentTarget.select()}
							className="min-w-0 flex-1"
						/>
						<Button
							type="button"
							variant="outline"
							onClick={() => void handleCopy()}
						>
							Copy public link
						</Button>
						<Button variant="outline" asChild>
							<a href={publicUrl} target="_blank" rel="noreferrer">
								Open page
							</a>
						</Button>
					</div>
					{copied ? (
						<p role="status" className="text-sm text-muted-foreground">
							Public link copied
						</p>
					) : null}
				</div>
			) : null}

			<div className="space-y-1.5">
				<Label htmlFor="status-page-title">Title</Label>
				<Input
					id="status-page-title"
					value={title}
					aria-invalid={titleError ? true : undefined}
					aria-describedby={titleError ? 'status-page-title-error' : undefined}
					onChange={(event) => {
						setTitle(event.target.value);
						setValidation(null);
					}}
				/>
				{titleError ? (
					<p id="status-page-title-error" className="text-sm text-destructive">
						{titleError}
					</p>
				) : null}
			</div>

			<div className="space-y-1.5">
				<Label htmlFor="status-page-description">Description</Label>
				<Textarea
					id="status-page-description"
					value={description}
					maxLength={DESCRIPTION_MAX}
					aria-invalid={descriptionError ? true : undefined}
					aria-describedby={
						descriptionError ? 'status-page-description-error' : undefined
					}
					onChange={(event) => {
						setDescription(event.target.value);
						setValidation(null);
					}}
				/>
				<div className="flex items-center justify-between gap-2">
					{descriptionError ? (
						<p
							id="status-page-description-error"
							className="text-sm text-destructive"
						>
							{descriptionError}
						</p>
					) : (
						<span />
					)}
					<span className="text-xs text-muted-foreground">
						{description.length}/{DESCRIPTION_MAX}
					</span>
				</div>
			</div>

			<fieldset className="space-y-3">
				<legend className="text-sm font-medium">Monitors</legend>
				{monitors.length === 0 ? (
					<p className="text-sm text-muted-foreground">
						Create a monitor before publishing a status page.{' '}
						<a
							href={`/w/${encodeURIComponent(workspace.slug)}/monitors/new`}
							className="underline underline-offset-4"
						>
							Create a monitor
						</a>
					</p>
				) : (
					<ul className="space-y-2">
						{monitors.map((monitor) => (
							<li
								key={monitor.id}
								className="flex items-center justify-between gap-3 rounded-lg border p-3"
							>
								<div className="flex items-center gap-2">
									<input
										type="checkbox"
										id={`status-page-monitor-${monitor.id}`}
										aria-label={`Publish ${monitor.name}`}
										checked={selectedIds.has(monitor.id)}
										onChange={(event) =>
											toggleMonitor(monitor.id, event.target.checked)
										}
										className="size-4 rounded border-input"
									/>
									<label
										htmlFor={`status-page-monitor-${monitor.id}`}
										className="text-sm font-medium"
									>
										{monitor.name}
									</label>
								</div>
								<Badge variant="outline">
									{MONITOR_STATUS_LABELS[monitor.status]}
								</Badge>
							</li>
						))}
					</ul>
				)}
				{monitorsError ? (
					<p className="text-sm text-destructive">{monitorsError}</p>
				) : null}
			</fieldset>

			{validation ? (
				<p role="alert" className="text-sm text-destructive">
					{validation}
				</p>
			) : null}

			<div>
				<Button type="button" disabled={update.isPending} onClick={handleSave}>
					{update.isPending ? 'Saving…' : 'Save status page'}
				</Button>
			</div>
		</section>
	);
}
