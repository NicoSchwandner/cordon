<script lang="ts">
	let { status }: { status: string } = $props();

	const styles: Record<string, { dot: string; text: string }> = {
		running: { dot: 'bg-success-text', text: 'text-success-text' },
		suspended: { dot: 'bg-warning-text', text: 'text-warning-text' },
		creating: { dot: 'bg-info-text', text: 'text-info-text' },
		destroyed: { dot: 'bg-foreground-faint', text: 'text-foreground-faint' }
	};

	const style = $derived(styles[status] ?? { dot: 'bg-foreground-faint', text: 'text-foreground-muted' });
</script>

<span class="inline-flex items-center gap-1.5 text-sm {style.text}">
	<span class="inline-block h-2 w-2 rounded-full {style.dot}"></span>
	{status}
</span>
