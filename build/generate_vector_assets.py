"""AI_MIDI 蓝图级矢量图标与透明启动图生成器 (v3.0.0 PRO 精简高质感版)。
- 图标：音符主体极大化放大，高识别度，全尺寸通透。
- 启动图：大幅精简去噪，无多余条目，极简现代蓝图美学。
"""
import math
import os
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont, ImageFilter

THEMES = {
    "dark": {
        "bg": (10, 20, 36, 255),           # 极夜暗蓝 #0A1424
        "card_bg": (14, 26, 46, 255),      # 深蓝卡片底
        "grid": (0, 240, 255, 3),          # 极低调微弱网格 (~1.1% 透明度，几乎隐形)
        "grid_major": (0, 240, 255, 7),    # 主参考线 (~2.7% 超低透明度)
        "primary": (0, 240, 255, 255),     # 电光青 #00F0FF
        "primary_glow": (0, 240, 255, 100),
        "accent": (255, 94, 0, 255),       # 工业高光橙 #FF5E00
        "ink_light": (235, 248, 255, 255), # 纯净高亮白青
        "ink_muted": (120, 160, 195, 255), # 蓝灰说明文字
        "border": (0, 240, 255, 90),       # 纤细线框
        "border_strong": (0, 240, 255, 200),
        "black_keys": (6, 14, 26, 255),
        "shadow": (0, 0, 0, 160),
    },
    "warm": {
        "bg": (245, 243, 238, 255),        # 暖米白纸 #F5F3EE
        "card_bg": (250, 248, 243, 255),
        "grid": (65, 105, 225, 3),         # 极淡微弱网格 (~1.1% 透明度)
        "grid_major": (65, 105, 225, 6),   # 主参考线 (~2.3% 超低透明度)
        "primary": (65, 105, 225, 255),    # 皇家宝蓝 #4169E1
        "primary_glow": (65, 105, 225, 100),
        "accent": (211, 84, 0, 255),       # 工业赤橙 #D35400
        "ink_light": (25, 48, 80, 255),    # 深墨蓝标题
        "ink_muted": (110, 140, 175, 255),
        "border": (65, 105, 225, 80),
        "border_strong": (30, 58, 95, 200),
        "black_keys": (20, 38, 62, 255),
        "shadow": (80, 60, 40, 120),
    }
}


def get_font(size: int, bold: bool = False):
    font_names = [
        "segoeuib.ttf" if bold else "segoeui.ttf",
        "arialbd.ttf" if bold else "arial.ttf",
        "msyhbd.ttc" if bold else "msyh.ttc",
        "consolab.ttf" if bold else "consola.ttf"
    ]
    for name in font_names:
        try:
            return ImageFont.truetype(name, size)
        except (OSError, IOError):
            continue
    return ImageFont.load_default()


def draw_corner_brackets(draw: ImageDraw.ImageDraw, x1, y1, x2, y2, length, width, color):
    draw.line([(x1, y1), (x1 + length, y1)], fill=color, width=width)
    draw.line([(x1, y1), (x1, y1 + length)], fill=color, width=width)
    draw.line([(x2, y1), (x2 - length, y1)], fill=color, width=width)
    draw.line([(x2, y1), (x2, y1 + length)], fill=color, width=width)
    draw.line([(x1, y2), (x1 + length, y2)], fill=color, width=width)
    draw.line([(x1, y2), (x1, y2 - length)], fill=color, width=width)
    draw.line([(x2, y2), (x2 - length, y2)], fill=color, width=width)
    draw.line([(x2, y2), (x2 - length, y2)], fill=color, width=width)


# =========================================================================
# 1. 渲染 1024x1024 EXE 应用图标 (音符极大化，占画幅 75%+)
# =========================================================================
def render_app_icon(theme_name: str = "dark") -> Image.Image:
    t = THEMES[theme_name]
    scale = 4
    W, H = 1024 * scale, 1024 * scale

    canvas = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    draw = ImageDraw.Draw(canvas)

    # 1.1 外层微圆角实体方块
    pad = 40 * scale
    radius = 190 * scale
    draw.rounded_rectangle([pad, pad, W - pad, H - pad], radius=radius, fill=t["bg"], outline=t["border_strong"], width=4 * scale)

    # 1.2 测绘准星
    draw_corner_brackets(draw, pad + 24 * scale, pad + 24 * scale, W - pad - 24 * scale, H - pad - 24 * scale, length=40 * scale, width=4 * scale, color=t["primary"])

    # 1.4 极大化居中音符图腾 (占用整体画幅约 80%，垂直完全居中)
    cx, cy = W // 2, H // 2

    # 音符几何参数 (超大尺寸，饱满有力)
    n1_x, n1_y = cx - 180 * scale, cy + 160 * scale
    n2_x, n2_y = cx + 170 * scale, cy + 70 * scale
    stem_top_y = cy - 290 * scale
    stem_w = 46 * scale
    head_rx, head_ry = 110 * scale, 84 * scale

    # 符头 (双层高质感发光)
    # 符头 1
    draw.ellipse([n1_x - head_rx, n1_y - head_ry, n1_x + head_rx, n1_y + head_ry], fill=t["card_bg"], outline=t["primary"], width=9 * scale)
    draw.ellipse([n1_x - head_rx * 0.55, n1_y - head_ry * 0.55, n1_x + head_rx * 0.55, n1_y + head_ry * 0.55], fill=t["accent"])
    
    # 符头 2
    draw.ellipse([n2_x - head_rx, n2_y - head_ry, n2_x + head_rx, n2_y + head_ry], fill=t["card_bg"], outline=t["primary"], width=9 * scale)
    draw.ellipse([n2_x - head_rx * 0.55, n2_y - head_ry * 0.55, n2_x + head_rx * 0.55, n2_y + head_ry * 0.55], fill=t["accent"])

    # 符干 (粗实有力)
    s1_x = n1_x + head_rx - stem_w
    s2_x = n2_x + head_rx - stem_w
    draw.rectangle([s1_x, stem_top_y, s1_x + stem_w, n1_y], fill=t["primary"])
    draw.rectangle([s2_x, stem_top_y, s2_x + stem_w, n2_y], fill=t["primary"])

    # 双十六分音符横梁
    beam1_h = 50 * scale
    beam2_h = 32 * scale
    draw.polygon([
        (s1_x, stem_top_y),
        (s2_x + stem_w, stem_top_y - 20 * scale),
        (s2_x + stem_w, stem_top_y - 20 * scale + beam1_h),
        (s1_x, stem_top_y + beam1_h)
    ], fill=t["primary"])

    draw.polygon([
        (s1_x, stem_top_y + beam1_h + 18 * scale),
        (s2_x + stem_w, stem_top_y - 20 * scale + beam1_h + 18 * scale),
        (s2_x + stem_w, stem_top_y - 20 * scale + beam1_h + 18 * scale + beam2_h),
        (s1_x, stem_top_y + beam1_h + 18 * scale + beam2_h)
    ], fill=t["accent"])

    # 4x 超采样缩小回目标 1024x1024 分辨率
    return canvas.resize((1024, 1024), Image.Resampling.LANCZOS)


# =========================================================================
# 2. 渲染蓝图风格精简无边框 Splash 启动图 (780 x 420 黄金画幅)
# =========================================================================
def render_blueprint_splash(theme_name: str = "dark") -> Image.Image:
    t = THEMES[theme_name]
    scale = 4
    W, H = 780 * scale, 420 * scale

    canvas = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    draw = ImageDraw.Draw(canvas)

    # 2.1 外层圆角卡片底板
    margin = 12 * scale
    card_x1, card_y1 = margin, margin
    card_x2, card_y2 = W - margin, H - margin
    radius = 14 * scale

    draw.rounded_rectangle([card_x1, card_y1, card_x2, card_y2], radius=radius, fill=t["bg"], outline=t["border_strong"], width=3 * scale)

    # 2.2 四角测绘准星
    draw_corner_brackets(
        draw,
        card_x1 + 18 * scale, card_y1 + 18 * scale,
        card_x2 - 18 * scale, card_y2 - 18 * scale,
        length=24 * scale, width=3 * scale, color=t["primary"]
    )

    # =====================================================================
    # 2.4 左半区：清晰发光音符主体与琴键底座
    # =====================================================================
    left_cx = card_x1 + int((card_x2 - card_x1) * 0.28)
    left_cy = card_y1 + int((card_y2 - card_y1) * 0.44)

    # 琴键底座
    pk_w, pk_h = 200 * scale, 44 * scale
    pk_x = left_cx - pk_w // 2
    pk_y = left_cy + 75 * scale
    draw.rectangle([pk_x, pk_y, pk_x + pk_w, pk_y + pk_h], fill=t["card_bg"], outline=t["primary"], width=2 * scale)

    num_white = 8
    kw = pk_w / num_white
    for i in range(1, num_white):
        draw.line([(pk_x + i * kw, pk_y), (pk_x + i * kw, pk_y + pk_h)], fill=t["border"], width=1 * scale)

    black_indices = [1, 2, 4, 5, 6]
    for bi in black_indices:
        bx = pk_x + bi * kw - kw * 0.35
        draw.rectangle([bx, pk_y, bx + kw * 0.7, pk_y + pk_h * 0.62], fill=t["black_keys"], outline=t["accent"], width=2 * scale)

    # 发光双音符
    n1_x, n1_y = left_cx - 50 * scale, left_cy + 25 * scale
    n2_x, n2_y = left_cx + 45 * scale, left_cy + 5 * scale
    stem_top_y = left_cy - 100 * scale
    stem_w = 16 * scale
    head_r = 22 * scale

    draw.ellipse([n1_x - head_r, n1_y - head_r * 0.75, n1_x + head_r, n1_y + head_r * 0.75], fill=t["card_bg"], outline=t["primary"], width=3 * scale)
    draw.ellipse([n1_x - head_r * 0.55, n1_y - head_r * 0.4, n1_x + head_r * 0.55, n1_y + head_r * 0.4], fill=t["accent"])

    draw.ellipse([n2_x - head_r, n2_y - head_r * 0.75, n2_x + head_r, n2_y + head_r * 0.75], fill=t["card_bg"], outline=t["primary"], width=3 * scale)
    draw.ellipse([n2_x - head_r * 0.55, n2_y - head_r * 0.4, n2_x + head_r * 0.55, n2_y + head_r * 0.4], fill=t["accent"])

    # 符干
    s1_x = n1_x + head_r - stem_w
    s2_x = n2_x + head_r - stem_w
    draw.rectangle([s1_x, stem_top_y, s1_x + stem_w, n1_y], fill=t["primary"])
    draw.rectangle([s2_x, stem_top_y, s2_x + stem_w, n2_y], fill=t["primary"])

    # 双横梁
    draw.polygon([
        (s1_x, stem_top_y), (s2_x + stem_w, stem_top_y),
        (s2_x + stem_w, stem_top_y + 16 * scale), (s1_x, stem_top_y + 16 * scale)
    ], fill=t["primary"])

    draw.polygon([
        (s1_x, stem_top_y + 22 * scale), (s2_x + stem_w, stem_top_y + 22 * scale),
        (s2_x + stem_w, stem_top_y + 34 * scale), (s1_x, stem_top_y + 34 * scale)
    ], fill=t["accent"])

    # =====================================================================
    # 2.5 右半区：精简高端品牌大标与副标题 (去除杂乱列表)
    # =====================================================================
    rx = card_x1 + int((card_x2 - card_x1) * 0.52)
    font_brand = get_font(58 * scale, bold=True)
    font_sub = get_font(18 * scale, bold=True)
    font_desc = get_font(14 * scale, bold=False)
    font_tag = get_font(12 * scale, bold=True)
    font_mono = get_font(11 * scale, bold=False)

    # 顶部标签印章: [ v3.0.0 PRO ] [ AUTONOMOUS HARNESS ]
    tag_y = card_y1 + 95 * scale
    
    t1_text = "v3.0.0 PRO"
    t1_bbox = draw.textbbox((rx, tag_y), t1_text, font=font_tag)
    pad_t = 4 * scale
    draw.rectangle([t1_bbox[0] - pad_t, t1_bbox[1] - pad_t, t1_bbox[2] + pad_t, t1_bbox[3] + pad_t], fill=t["card_bg"], outline=t["accent"], width=1 * scale)
    draw.text((rx, tag_y), t1_text, font=font_tag, fill=t["accent"])

    t2_x = t1_bbox[2] + pad_t * 4
    t2_text = "AUTONOMOUS HARNESS"
    t2_bbox = draw.textbbox((t2_x, tag_y), t2_text, font=font_tag)
    draw.rectangle([t2_bbox[0] - pad_t, t2_bbox[1] - pad_t, t2_bbox[2] + pad_t, t2_bbox[3] + pad_t], fill=t["card_bg"], outline=t["border"], width=1 * scale)
    draw.text((t2_x, tag_y), t2_text, font=font_tag, fill=t["primary"])

    # 主品牌名称: AI_MIDI
    brand_y = tag_y + 40 * scale
    draw.text((rx, brand_y), "AI_MIDI", font=font_brand, fill=t["ink_light"])

    # 副标题
    sub_y = brand_y + 74 * scale
    draw.text((rx + 2 * scale, sub_y), "PRO AUDIO HARNESS & INTELLIGENCE", font=font_sub, fill=t["primary"])

    # =====================================================================
    # 2.6 底部：极简发光加载条与状态栏
    # =====================================================================
    divider_y = card_y2 - 64 * scale
    draw.line([(card_x1 + 20 * scale, divider_y), (card_x2 - 20 * scale, divider_y)], fill=t["border"], width=1 * scale)

    # 加载条
    bar_y = divider_y + 14 * scale
    bar_w = (card_x2 - card_x1) - 40 * scale
    bar_x = card_x1 + 20 * scale
    bar_h = 4 * scale

    draw.rectangle([bar_x, bar_y, bar_x + bar_w, bar_y + bar_h], fill=t["card_bg"], outline=t["border"], width=1 * scale)
    fill_w = int(bar_w * 0.85)
    draw.rectangle([bar_x, bar_y, bar_x + fill_w, bar_y + bar_h], fill=t["primary"])
    draw.rectangle([bar_x + fill_w - 4 * scale, bar_y - 2 * scale, bar_x + fill_w + 4 * scale, bar_y + bar_h + 2 * scale], fill=t["accent"])

    # 底部状态文字
    status_y = bar_y + 14 * scale
    draw.text((bar_x + 2 * scale, status_y), "INITIALIZING AUDIO HARNESS... [ 480 TPB // 120 BPM ]", font=font_mono, fill=t["ink_muted"])
    
    right_status = "ENGINE: GO + WAILS v3.0.0"
    rbox = draw.textbbox((0, 0), right_status, font=font_mono)
    draw.text((bar_x + bar_w - (rbox[2] - rbox[0]) - 2 * scale, status_y), right_status, font=font_mono, fill=t["primary"])

    # 外层立体阴影合成
    shadow_layer = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    sdraw = ImageDraw.Draw(shadow_layer)
    sdraw.rounded_rectangle([card_x1, card_y1, card_x2, card_y2], radius=radius, fill=t["shadow"])
    shadow_blur = shadow_layer.filter(ImageFilter.GaussianBlur(radius=10 * scale))

    final_img = Image.new("RGBA", (W, H), (0, 0, 0, 0))
    final_img.paste(shadow_blur, (0, 4 * scale), shadow_blur)
    final_img.paste(canvas, (0, 0), canvas)

    # 导出 1560x840 (2x 高清母图，完美适应 100%~250% 各类高 DPI 屏幕缩放)
    return final_img.resize((1560, 840), Image.Resampling.LANCZOS)


def save_multi_size_ico(img_1024: Image.Image, output_ico_path: Path):
    sizes = [(256, 256), (128, 128), (64, 64), (48, 48), (32, 32), (16, 16)]
    img_1024.save(output_ico_path, format="ICO", sizes=sizes)
    print(f"[OK] Generated Windows ICO: {output_ico_path.name}")


def main():
    root = Path(__file__).resolve().parent.parent
    build_dir = root / "build"
    frontend_dir = root / "frontend"
    build_windows_dir = build_dir / "windows"
    winres_dir = root / "winres"

    os.makedirs(build_dir, exist_ok=True)
    os.makedirs(build_windows_dir, exist_ok=True)
    os.makedirs(frontend_dir, exist_ok=True)
    os.makedirs(winres_dir, exist_ok=True)

    # 1. 渲染冷色蓝图 (Dark Cyber-Blueprint)
    print("[1/3] Rendering Dark Cyber-Blueprint v3.0 (Enlarged Note Icon & Clean Splash)...")
    dark_icon = render_app_icon("dark")
    dark_splash = render_blueprint_splash("dark")

    dark_icon.save(build_dir / "app_icon_dark.png", format="PNG")
    dark_icon.save(build_dir / "appicon.png", format="PNG")
    dark_icon.save(frontend_dir / "app_icon_dark.png", format="PNG")
    dark_icon.save(frontend_dir / "app_icon.png", format="PNG")

    dark_splash.save(build_dir / "splash_dark.png", format="PNG")
    dark_splash.save(build_dir / "splash.png", format="PNG")
    dark_splash.save(root / "splash.png", format="PNG")
    dark_splash.save(root / "splash_dark.png", format="PNG")
    dark_splash.save(frontend_dir / "splash_dark.png", format="PNG")
    dark_splash.save(frontend_dir / "splash.png", format="PNG")

    save_multi_size_ico(dark_icon, build_windows_dir / "icon.ico")
    save_multi_size_ico(dark_icon, build_dir / "app_icon_dark.ico")
    save_multi_size_ico(dark_icon, root / "app_icon_dark.ico")
    save_multi_size_ico(dark_icon, root / "app_icon.ico")
    save_multi_size_ico(dark_icon, root / "window_icon.ico")
    save_multi_size_ico(dark_icon, frontend_dir / "favicon_dark.ico")
    save_multi_size_ico(dark_icon, frontend_dir / "favicon.ico")

    # 2. 渲染暖色图纸 (Warm Parchment)
    print("[2/3] Rendering Warm Parchment v3.0 (Enlarged Note Icon & Clean Splash)...")
    warm_icon = render_app_icon("warm")
    warm_splash = render_blueprint_splash("warm")

    warm_icon.save(build_dir / "app_icon_warm.png", format="PNG")
    warm_icon.save(frontend_dir / "app_icon_warm.png", format="PNG")

    warm_splash.save(build_dir / "splash_warm.png", format="PNG")
    warm_splash.save(root / "splash_warm.png", format="PNG")
    warm_splash.save(frontend_dir / "splash_warm.png", format="PNG")

    save_multi_size_ico(warm_icon, build_dir / "app_icon_warm.ico")
    save_multi_size_ico(warm_icon, root / "app_icon_warm.ico")
    save_multi_size_ico(warm_icon, frontend_dir / "favicon_warm.ico")

    # 导出各分辨率切片供 Windows PE 资源编译器打包
    for sz in [256, 128, 64, 48, 32, 16]:
        resized = dark_icon.resize((sz, sz), Image.Resampling.LANCZOS)
        resized.save(build_dir / f"icon_{sz}.png", format="PNG")

    print("[3/3] All v3.0.0 Refined Blueprint assets generated successfully!")


if __name__ == "__main__":
    main()
