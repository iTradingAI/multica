import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { useT } from "@multica/views/i18n";

export function PageLoading() {
  const { t } = useT("common");
  return (
    <div
      data-route-pending=""
      role="status"
      aria-label={t(($) => $.loading)}
      className="flex flex-1 items-center justify-center"
    >
      <MulticaIcon className="size-6 animate-pulse" />
    </div>
  );
}
