import { MulticaIcon } from "@multica/ui/components/common/multica-icon";

export function PageLoading() {
  return (
    <div
      data-route-pending=""
      role="status"
      aria-label="Loading page"
      className="flex flex-1 items-center justify-center"
    >
      <MulticaIcon className="size-6 animate-pulse" />
    </div>
  );
}
