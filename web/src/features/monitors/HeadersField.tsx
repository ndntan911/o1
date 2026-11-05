import type { UseFormReturn } from 'react-hook-form';
import { useFieldArray, useWatch } from 'react-hook-form';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import type { MonitorFormValues } from './schemas';

export interface HeadersFieldProps {
	form: UseFormReturn<MonitorFormValues>;
	mode: 'create' | 'edit';
	existingHeaderNames: string[];
	replaceHeaders: boolean;
	onReplaceHeadersChange: (replaceHeaders: boolean) => void;
	error?: string;
}

export function HeadersField({
	form,
	mode,
	existingHeaderNames,
	replaceHeaders,
	onReplaceHeadersChange,
	error,
}: HeadersFieldProps) {
	const { fields, append, remove } = useFieldArray({
		control: form.control,
		name: 'headers',
	});
	const watchedHeaders = useWatch({ control: form.control, name: 'headers' });

	const editable = mode === 'create' || replaceHeaders;
	const hasNamedHeader = (watchedHeaders ?? []).some(
		(row) => row.name.trim() !== '',
	);

	return (
		<section className="space-y-3">
			<div className="flex items-center justify-between gap-3">
				<h2 className="font-heading text-sm font-semibold">Headers</h2>
				{mode === 'edit' ? (
					<div className="flex items-center gap-2">
						<Label htmlFor="replace-headers">Replace headers</Label>
						<Switch
							id="replace-headers"
							aria-label="Replace headers"
							checked={replaceHeaders}
							onCheckedChange={onReplaceHeadersChange}
						/>
					</div>
				) : null}
			</div>

			{mode === 'edit' ? (
				<div className="space-y-1 text-sm">
					<p className="text-muted-foreground">Existing headers</p>
					{existingHeaderNames.length > 0 ? (
						<ul className="flex flex-wrap gap-x-3 gap-y-1 font-mono text-xs">
							{existingHeaderNames.map((name) => (
								<li key={name}>{name}</li>
							))}
						</ul>
					) : (
						<p className="text-muted-foreground">None</p>
					)}
					{!replaceHeaders ? (
						<p className="text-xs text-muted-foreground">
							Header values stay hidden and are left unchanged unless you
							replace them.
						</p>
					) : null}
				</div>
			) : null}

			{replaceHeaders && !hasNamedHeader ? (
				<p className="text-sm text-destructive" role="status">
					Saving will remove all headers.
				</p>
			) : null}

			{editable ? (
				<div className="space-y-2">
					{fields.map((field, index) => (
						<div key={field.id} className="flex items-end gap-2">
							<div className="flex-1 space-y-1">
								<Label htmlFor={`header-${index}-name`}>
									Header {index + 1} name
								</Label>
								<Input
									id={`header-${index}-name`}
									aria-label={`Header ${index + 1} name`}
									placeholder="Authorization"
									autoComplete="off"
									{...form.register(`headers.${index}.name`)}
								/>
							</div>
							<div className="flex-1 space-y-1">
								<Label htmlFor={`header-${index}-value`}>
									Header {index + 1} value
								</Label>
								<Input
									id={`header-${index}-value`}
									aria-label={`Header ${index + 1} value`}
									placeholder="Bearer token"
									autoComplete="off"
									{...form.register(`headers.${index}.value`)}
								/>
							</div>
							<Button
								type="button"
								variant="ghost"
								size="sm"
								aria-label={`Remove header ${index + 1}`}
								onClick={() => remove(index)}
							>
								Remove
							</Button>
						</div>
					))}

					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => append({ name: '', value: '' })}
						disabled={fields.length >= 20}
					>
						Add header
					</Button>
				</div>
			) : null}

			{error ? <p className="text-sm text-destructive">{error}</p> : null}
		</section>
	);
}
