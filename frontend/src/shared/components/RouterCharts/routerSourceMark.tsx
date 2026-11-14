import { ExternalIcon, ExternalIconType } from "../atoms/ExternalIcon";
import { Icon } from "../atoms/Icon";
import { Tooltip } from "../molecules/Tooltip";

/** Marks a card's figures as Weave Router data, next to the card title. */
export const ROUTER_SOURCE_MARK = (
  <ExternalIcon type={ExternalIconType.WeaveRouter} className="size-5" />
);

/** The badged Router mark hosted metric charts draw from their metric's data sources. */
export const ROUTER_METRIC_SOURCE_MARK = (
  <div className="flex shrink-0 items-center">
    <div className="isolate flex shrink-0 items-center">
      <div className="relative -mr-1.5 flex items-center justify-center last-of-type:mr-0">
        <Tooltip content="Weave Router" className="rounded-full">
          <Icon.Badge>
            <ExternalIcon type={ExternalIconType.WeaveRouter} className="size-4" />
          </Icon.Badge>
        </Tooltip>
      </div>
    </div>
  </div>
);
