# 内置前端组件

FilePanel 使用原生 HTML、CSS 和 JavaScript，以下免费组件随 Go 二进制一起提供，无外部 CDN、字体请求或 React 运行时。

| 来源 | 固定版本 | 使用范围 | 许可 |
| --- | --- | --- | --- |
| [Basecoat](https://github.com/hunvreus/basecoat/tree/0.3.4) | 0.3.4 | `basecoat-components.css`：Button 与 Tabs 的视觉规则适配；将 Tailwind utilities 展开为 CSS，颜色接入 FilePanel Tokens。视图切换作用于同一文件区域，因此使用 `aria-pressed` 按钮组。 | [MIT](basecoat-LICENSE.txt) |
| [Lucide](https://github.com/lucide-icons/lucide/tree/1.61.0) | 1.61.0 | `../icons.svg`：从官方 SVG 源文件提取所用图标，组合为本地 symbol sprite。 | [ISC 及继承图标许可](lucide-LICENSE.txt) |

视觉布局参考 [Arc UI](https://uiarc.dev/) 的留白、卡片与控件层级，以及 [React Bits](https://reactbits.dev/) 的轻量反馈思路；未复制其付费组件或引入其运行库。淡入、悬停、焦点与状态反馈使用原生 CSS，遵循减少动态效果的系统偏好。文件选择、分享权限、下载线路与个性化仍由 FilePanel 原有业务实现。
