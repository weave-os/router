import {
  Activity,
  AlarmClockOff,
  Archive,
  AreaChart,
  ArrowDown,
  ArrowDownRight,
  ArrowLeft,
  ArrowLeftFromLine,
  ArrowRight,
  ArrowRightToLine,
  ArrowUp,
  ArrowUpRight,
  Asterisk,
  Banknote,
  BarChart,
  Bell,
  Bird,
  Book,
  Bot,
  Brain,
  Briefcase,
  Bug,
  Building2,
  Calendar,
  CalendarClock,
  CalendarRange,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronsUpDown,
  ChevronUp,
  Circle,
  CircleAlert,
  CircleCheck,
  CircleCheckBig,
  CircleDashed,
  CircleGauge,
  CircleHelp,
  CircleUserRound,
  CircleX,
  Clipboard,
  ClipboardPen,
  Clock,
  Clock3,
  Code,
  Command,
  Construction,
  CopyCheck,
  CornerDownLeft,
  CreditCard,
  Crown,
  Database,
  Download,
  Ellipsis,
  ExternalLink,
  Eye,
  EyeOff,
  File,
  FileText,
  Filter,
  Flag,
  Flame,
  FlaskConical,
  Focus,
  Folder,
  GanttChart,
  GitBranch,
  Github,
  Gitlab,
  GitMerge,
  GitPullRequest,
  Glasses,
  Globe,
  GripVertical,
  Group,
  History,
  Home,
  Info,
  Kanban,
  KeyRound,
  Landmark,
  Laptop,
  Layers2,
  Lightbulb,
  LineChart,
  Link,
  ListCheck,
  Loader2,
  Lock,
  LogIn,
  LogOut,
  LucideProps,
  Mail,
  Megaphone,
  MessageCircleMore,
  Minus,
  Moon,
  MoveDiagonal,
  Network,
  PanelLeft,
  PanelRightClose,
  Pencil,
  PieChart,
  Pin,
  PinOff,
  Plus,
  RefreshCcw,
  Rocket,
  Rows3,
  ScatterChart,
  Search,
  Send,
  Server,
  Settings,
  Share2,
  Shield,
  Slack,
  SlidersHorizontal,
  SortAsc,
  SortDesc,
  Sparkles,
  Square,
  SquarePen,
  SquareStack,
  Star,
  Sun,
  Tag,
  Terminal,
  ThumbsDown,
  ThumbsUp,
  Trash,
  Triangle,
  TriangleAlert,
  Tv,
  Undo2,
  Upload,
  UserRound,
  UsersRound,
  Wrench,
  X,
  Zap,
  ZoomIn,
} from "lucide-react";

import { type ComponentType } from "react";

import { cn } from "../../../tools/cn";
import { Intent } from "../../types";
import { RobotIcon } from "./RobotIcon";

/**
 * All the possible icons we support rendering.
 */
export type IconType =
  | "Activity"
  | "AlarmClockOff"
  | "Archive"
  | "AreaChart"
  | "ArrowDown"
  | "ArrowDownRight"
  | "ArrowLeft"
  | "ArrowLeftFromLine"
  | "ArrowRight"
  | "ArrowRightToLine"
  | "ArrowUp"
  | "ArrowUpRight"
  | "Asterisk"
  | "Banknote"
  | "BarChart"
  | "Bell"
  | "Bird"
  | "Book"
  | "Bot"
  | "Brain"
  | "Briefcase"
  | "Bug"
  | "Calendar"
  | "CalendarClock"
  | "CalendarRange"
  | "Chat"
  | "Check"
  | "ChevronDown"
  | "ChevronLeft"
  | "ChevronRight"
  | "ChevronsUpDown"
  | "ChevronUp"
  | "Circle"
  | "CircleAlert"
  | "CircleCheck"
  | "CircleCheckSmall"
  | "CircleDashed"
  | "CircleQuestion"
  | "CircleUser"
  | "CircleX"
  | "Clipboard"
  | "ClipboardPen"
  | "Clock"
  | "Clock3"
  | "Code"
  | "Command"
  | "Construction"
  | "CopyCheck"
  | "CornerDownLeft"
  | "CreditCard"
  | "Crown"
  | "Database"
  | "Download"
  | "Ellipsis"
  | "Expand"
  | "ExternalLink"
  | "Eye"
  | "EyeOff"
  | "File"
  | "FileText"
  | "Filter"
  | "Flag"
  | "Flame"
  | "FlaskConical"
  | "Focus"
  | "Folder"
  | "GanttChart"
  | "Gauge"
  | "GitBranch"
  | "GitHub"
  | "GitLab"
  | "GitMerge"
  | "GitPullRequest"
  | "Glasses"
  | "Globe"
  | "GripVertical"
  | "Group"
  | "History"
  | "Home"
  | "Info"
  | "Kanban"
  | "Key"
  | "Landmark"
  | "Laptop"
  | "Layers"
  | "Lightbulb"
  | "LineChart"
  | "Link"
  | "ListCheck"
  | "Loading"
  | "Lock"
  | "LogIn"
  | "LogOut"
  | "Mail"
  | "Megaphone"
  | "Minus"
  | "Moon"
  | "Network"
  | "Organization"
  | "PanelLeft"
  | "PanelRightClose"
  | "Pencil"
  | "PieChart"
  | "Pin"
  | "PinOff"
  | "Plus"
  | "Refresh"
  | "Robot"
  | "Rocket"
  | "Rows3"
  | "ScatterChart"
  | "Search"
  | "Send"
  | "Server"
  | "Settings"
  | "Share"
  | "Shield"
  | "Slack"
  | "SlidersHorizontal"
  | "SortAsc"
  | "SortDesc"
  | "Sparkles"
  | "Square"
  | "SquarePen"
  | "SquareStack"
  | "Star"
  | "Sun"
  | "Tag"
  | "Terminal"
  | "ThumbsDown"
  | "ThumbsUp"
  | "Trash"
  | "Triangle"
  | "TriangleAlert"
  | "Tv"
  | "Undo"
  | "Upload"
  | "User"
  | "Users"
  | "Wrench"
  | "X"
  | "Zap"
  | "ZoomIn";

const ICON_COMPONENTS: Record<IconType, ComponentType<LucideProps>> = {
  Activity,
  AlarmClockOff,
  Archive,
  AreaChart,
  ArrowDown,
  ArrowDownRight,
  ArrowLeft,
  ArrowLeftFromLine,
  ArrowRight,
  ArrowRightToLine,
  ArrowUp,
  ArrowUpRight,
  Asterisk,
  Banknote,
  BarChart,
  Bell,
  Bird,
  Book,
  Bot,
  Brain,
  Briefcase,
  Bug,
  Calendar,
  CalendarClock,
  CalendarRange,
  Chat: MessageCircleMore,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronsUpDown,
  ChevronUp,
  Circle,
  CircleAlert,
  CircleCheck: CircleCheckBig,
  CircleCheckSmall: CircleCheck,
  CircleDashed,
  CircleQuestion: CircleHelp,
  CircleUser: CircleUserRound,
  CircleX,
  Clipboard,
  ClipboardPen,
  Clock,
  Clock3,
  Code,
  Command,
  Construction,
  CopyCheck,
  CornerDownLeft,
  CreditCard,
  Crown,
  Database,
  Download,
  Ellipsis,
  Expand: MoveDiagonal,
  ExternalLink,
  Eye,
  EyeOff,
  File,
  FileText,
  Filter,
  Flag,
  Flame,
  FlaskConical,
  Focus,
  Folder,
  GanttChart,
  Gauge: CircleGauge,
  GitBranch,
  GitHub: Github,
  GitLab: Gitlab,
  GitMerge,
  GitPullRequest,
  Glasses,
  Globe,
  GripVertical,
  Group,
  History,
  Home,
  Info,
  Kanban,
  Key: KeyRound,
  Landmark,
  Laptop,
  Layers: Layers2,
  Lightbulb,
  LineChart,
  Link,
  ListCheck,
  Loading: Loader2,
  Lock,
  LogIn,
  LogOut,
  Mail,
  Megaphone,
  Minus,
  Moon,
  Network,
  Organization: Building2,
  PanelLeft,
  PanelRightClose,
  Pencil,
  PieChart,
  Pin,
  PinOff,
  Plus,
  Refresh: RefreshCcw,
  Robot: RobotIcon,
  Rocket,
  Rows3,
  ScatterChart,
  Search,
  Send,
  Server,
  Settings,
  Share: Share2,
  Shield,
  Slack,
  SlidersHorizontal,
  SortAsc,
  SortDesc,
  Sparkles,
  Square,
  SquarePen,
  SquareStack,
  Star,
  Sun,
  Tag,
  Terminal,
  ThumbsDown,
  ThumbsUp,
  Trash,
  Triangle,
  TriangleAlert,
  Tv,
  Undo: Undo2,
  Upload,
  User: UserRound,
  Users: UsersRound,
  Wrench,
  X,
  Zap,
  ZoomIn,
};

export interface IconProps extends LucideProps {
  /**
   * @default Intent.Default
   */
  intent?: Intent;
  type: IconType;
}

export function Icon({ className, intent = Intent.Default, type, ...iconProps }: IconProps) {
  const IconComponent = ICON_COMPONENTS[type];

  return (
    <IconComponent
      {...iconProps}
      className={cn(
        "size-4 shrink-0 grow-0",
        {
          "text-wv-status-danger": intent === Intent.Danger,
          "text-wv-action": intent === Intent.Primary,
          "text-wv-status-success": intent === Intent.Success,
          "text-wv-status-warning": intent === Intent.Warning,
        },
        className,
      )}
    />
  );
}

function IconBadge({ children }: React.PropsWithChildren) {
  return (
    <div className="light:shadow-sm surface-card flex shrink-0 grow-0 items-center justify-center rounded-full border p-1">
      {children}
    </div>
  );
}

/** Renders an icon with a nice badge around it. */
Icon.Badge = IconBadge;
