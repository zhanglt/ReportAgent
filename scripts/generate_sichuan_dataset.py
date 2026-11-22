# -*- coding: utf-8 -*-
"""
sichuan 测试数据集生成器
========================
依据 database.md（水文数据库表结构规范）与 NL2SQL/schema_cache/schema.json（实际表结构）
为 MySQL 库 sichuan 生成逻辑自洽的小时级合成测试数据。

范围约定
--------
* 行政区域：四川省 21 个市州（GB/T 2260 码 51xxxx），另含省级行 510000。
* 测站：每个市州 2 个河道水文站，共 42 站。
  - 第一站站码：SC + 6 位行政区划码，如 SC510100（成都）
  - 第二站站码：SC + 区划码前 4 位 + 01，如 SC510101
* 业务时间：2025-01-01 00:00:00 ~ 2026-12-31 23:00:00（730 天 × 24 小时 = 17520 小时/站）。
* ST_RIVER_R / ST_PPTN_R：每站每天 24 条整点记录。
  - ST_PPTN_R.TM 为时段截止时间，DRP 为 (TM-1h, TM] 的时段雨量；
  - DYP 仅在每日 08:00 且资料完整时填写（前一日 09:00 ~ 当日 08:00 共 24 个时段之和）；
  - INTV=1.00（HH.NN，1 小时）；PDR 为实际降雨历时 HH.NN（NN 为分钟），无雨为 0；
  - WTH：5 雪 / 6 雨夹雪 / 7 雨 / 8 阴 / 9 晴。
* 水文逻辑：
  - 降雨（季节分布 + 暴雨事件）→ 双线性水库汇流 → 流量 Q；
  - 水位由水位流量关系曲线 Z = Zref + (Q/a)^(1/b) 反推，保证 Z-Q 一致；
  - XSAVV = Q / XSA，XSMXV ≈ 1.22 × XSAVV；
  - WPTN 由水位涨落判定（4 落 / 5 涨 / 6 平）；FLWCHRCD 标记起涨(5)/洪峰(6)；
  - 防洪指标阈值满足：LAZ < SFZ < WRZ < TAZ < GRZ < LDKEL/RDKEL，
    LAQ < SFQ < WRQ < TAQ < GRQ，FLPQ < WRQ；全部 Z 阈值由同一关系曲线导出；
  - 实测最高/最低水位、最大/最小流量及其出现时间取自生成序列的真实极值；
  - 预警指标（ST_HIWRCH_B）：洪水蓝/黄/橙/红 = SF/WR/TA/GR 对应水位或流量；
    干旱阈值按低水流量递减设置。
* ST_FORECAST_F：每日 08:00 发布（依据时间 07:00），预报 +6h/+12h/+24h，
  预报值 = 对应时刻真实值 × 小误差；发生时间超出 2026-12-31 23:00 的记录不生成。
* 静态表 MODITIME 统一取数据集快照时间 2026-12-31 23:00:00。

用法
----
    python scripts/generate_sichuan_dataset.py [--seed 20250101] [--truncate]

连接参数默认读取项目 .env 的 DB_URI（此处直接用等价参数）。
--truncate：若目标表已有数据，先清空再写入（否则遇到非空表直接退出）。
"""
from __future__ import annotations

import argparse
import json
import math
import os
import sys
import time
from datetime import datetime, timedelta
from pathlib import Path

import numpy as np
import pymysql
from dotenv import load_dotenv

ROOT = Path(__file__).resolve().parents[1]

START = datetime(2025, 1, 1, 0, 0, 0)
DAYS = 730                      # 2025(365) + 2026(365)
HOURS = DAYS * 24               # 17520
END = START + timedelta(hours=HOURS - 1)
SNAPSHOT = datetime(2026, 12, 31, 23, 0, 0)   # 静态表时间戳 / 数据集快照

DB = dict(host="127.0.0.1", port=3306, user="root",
          database="sichuan", charset="utf8mb4")

# ---------------------------------------------------------------------------
# 市州与测站定义（坐标、河流、量级为模拟近似值，仅用于测试）
# size: L 大江大河 / M 中等河流 / S 中小河流
# z0: 常水位(m)  q0: 基准流量(m3/s)  drna: 集水面积(km2)  dstrvm: 至河口距离(km)
# ---------------------------------------------------------------------------
CITIES = [
    dict(code="510100", name="成都市", short="成都", region="basin", rain=950, phcd="CD",
         stations=[
             dict(stnm="成都锦江站", river="锦江", hnnm="岷江", size="M", z0=480.0, q0=60,
                  drna=1200, dstrvm=70, lon=104.0820, lat=30.6570, addr="成都市锦江区"),
             dict(stnm="成都青白江站", river="青白江", hnnm="沱江", size="M", z0=470.0, q0=45,
                  drna=900, dstrvm=180, lon=104.2510, lat=30.8790, addr="成都市青白江区"),
         ]),
    dict(code="510300", name="自贡市", short="自贡", region="basin", rain=1000, phcd="ZG",
         stations=[
             dict(stnm="自贡釜溪河站", river="釜溪河", hnnm="沱江", size="S", z0=277.0, q0=18,
                  drna=1500, dstrvm=110, lon=104.7784, lat=29.3390, addr="自贡市自流井区"),
             dict(stnm="自贡旭水河站", river="旭水河", hnnm="沱江", size="S", z0=285.0, q0=6,
                  drna=450, dstrvm=45, lon=104.7170, lat=29.3530, addr="自贡市贡井区"),
         ]),
    dict(code="510400", name="攀枝花市", short="攀枝花", region="panzhihua", rain=850, phcd="PZH",
         stations=[
             dict(stnm="攀枝花金沙江站", river="金沙江", hnnm="金沙江", size="L", z0=1002.0, q0=1500,
                  drna=259000, dstrvm=700, lon=101.7187, lat=26.5823, addr="攀枝花市东区"),
             dict(stnm="攀枝花雅砻江站", river="雅砻江", hnnm="雅砻江", size="L", z0=1010.0, q0=800,
                  drna=116000, dstrvm=35, lon=101.6200, lat=26.6200, addr="攀枝花市西区"),
         ]),
    dict(code="510500", name="泸州市", short="泸州", region="basin", rain=1100, phcd="LZ",
         stations=[
             dict(stnm="泸州长江站", river="长江", hnnm="长江干流", size="L", z0=230.8, q0=3200,
                  drna=480000, dstrvm=2300, lon=105.4423, lat=28.8718, addr="泸州市江阳区"),
             dict(stnm="泸州沱江站", river="沱江", hnnm="沱江", size="M", z0=235.0, q0=380,
                  drna=27800, dstrvm=2, lon=105.4350, lat=28.9000, addr="泸州市龙马潭区"),
         ]),
    dict(code="510600", name="德阳市", short="德阳", region="basin", rain=900, phcd="DY",
         stations=[
             dict(stnm="德阳石亭江站", river="石亭江", hnnm="沱江", size="S", z0=475.8, q0=25,
                  drna=1100, dstrvm=95, lon=104.3979, lat=31.1269, addr="德阳市旌阳区"),
             dict(stnm="德阳绵远河站", river="绵远河", hnnm="沱江", size="S", z0=490.0, q0=22,
                  drna=1300, dstrvm=80, lon=104.5100, lat=31.3200, addr="德阳市罗江区"),
         ]),
    dict(code="510700", name="绵阳市", short="绵阳", region="basin", rain=950, phcd="MY",
         stations=[
             dict(stnm="绵阳涪江桥站", river="涪江", hnnm="嘉陵江", size="M", z0=450.0, q0=280,
                  drna=12000, dstrvm=320, lon=104.6796, lat=31.4675, addr="绵阳市涪城区"),
             dict(stnm="绵阳安昌河站", river="安昌河", hnnm="嘉陵江", size="S", z0=465.0, q0=15,
                  drna=900, dstrvm=60, lon=104.4500, lat=31.5300, addr="绵阳市安州区"),
         ]),
    dict(code="510800", name="广元市", short="广元", region="basin", rain=1000, phcd="GY",
         stations=[
             dict(stnm="广元嘉陵江站", river="嘉陵江", hnnm="嘉陵江", size="M", z0=455.0, q0=210,
                  drna=26000, dstrvm=900, lon=105.8436, lat=32.4355, addr="广元市利州区"),
             dict(stnm="广元白龙江站", river="白龙江", hnnm="嘉陵江", size="M", z0=465.0, q0=110,
                  drna=14000, dstrvm=30, lon=105.7200, lat=32.3300, addr="广元市昭化区"),
         ]),
    dict(code="510900", name="遂宁市", short="遂宁", region="basin", rain=950, phcd="SN",
         stations=[
             dict(stnm="遂宁涪江站", river="涪江", hnnm="嘉陵江", size="M", z0=270.0, q0=350,
                  drna=23000, dstrvm=120, lon=105.5929, lat=30.5328, addr="遂宁市船山区"),
             dict(stnm="遂宁琼江站", river="琼江", hnnm="嘉陵江", size="S", z0=278.0, q0=12,
                  drna=1400, dstrvm=55, lon=105.4500, lat=30.3500, addr="遂宁市大英县"),
         ]),
    dict(code="511000", name="内江市", short="内江", region="basin", rain=1000, phcd="NJ",
         stations=[
             dict(stnm="内江沱江站", river="沱江", hnnm="沱江", size="M", z0=300.0, q0=250,
                  drna=17000, dstrvm=130, lon=105.0584, lat=29.5802, addr="内江市市中区"),
             dict(stnm="内江清流河站", river="清流河", hnnm="沱江", size="S", z0=310.0, q0=8,
                  drna=1000, dstrvm=40, lon=105.1500, lat=29.6200, addr="内江市东兴区"),
         ]),
    dict(code="511100", name="乐山市", short="乐山", region="basin", rain=1350, phcd="LS",
         stations=[
             dict(stnm="乐山岷江站", river="岷江", hnnm="岷江", size="L", z0=355.0, q0=2400,
                  drna=126000, dstrvm=120, lon=103.7657, lat=29.5521, addr="乐山市市中区"),
             dict(stnm="乐山大渡河站", river="大渡河", hnnm="岷江", size="L", z0=360.0, q0=1400,
                  drna=77000, dstrvm=8, lon=103.5500, lat=29.4200, addr="乐山市沙湾区"),
         ]),
    dict(code="511300", name="南充市", short="南充", region="basin", rain=1050, phcd="NC",
         stations=[
             dict(stnm="南充嘉陵江站", river="嘉陵江", hnnm="嘉陵江", size="L", z0=260.0, q0=700,
                  drna=76000, dstrvm=260, lon=106.1107, lat=30.8378, addr="南充市顺庆区"),
             dict(stnm="南充西充河站", river="西充河", hnnm="嘉陵江", size="S", z0=275.0, q0=7,
                  drna=900, dstrvm=60, lon=105.9000, lat=30.9900, addr="南充市西充县"),
         ]),
    dict(code="511400", name="眉山市", short="眉山", region="basin", rain=1100, phcd="MS",
         stations=[
             dict(stnm="眉山岷江站", river="岷江", hnnm="岷江", size="L", z0=410.0, q0=600,
                  drna=34000, dstrvm=200, lon=103.8485, lat=30.0754, addr="眉山市彭山区"),
             dict(stnm="眉山青衣江站", river="青衣江", hnnm="岷江", size="M", z0=445.0, q0=170,
                  drna=12000, dstrvm=90, lon=103.3700, lat=29.9000, addr="眉山市洪雅县"),
         ]),
    dict(code="511500", name="宜宾市", short="宜宾", region="basin", rain=1150, phcd="YB",
         stations=[
             dict(stnm="宜宾长江站", river="长江", hnnm="长江干流", size="L", z0=255.0, q0=6500,
                  drna=610000, dstrvm=2500, lon=104.6432, lat=28.7518, addr="宜宾市翠屏区"),
             dict(stnm="宜宾横江站", river="横江", hnnm="金沙江", size="M", z0=270.0, q0=90,
                  drna=15000, dstrvm=25, lon=104.5200, lat=28.6000, addr="宜宾市筠连县"),
         ]),
    dict(code="511600", name="广安市", short="广安", region="basin", rain=1100, phcd="GA",
         stations=[
             dict(stnm="广安渠江站", river="渠江", hnnm="嘉陵江", size="M", z0=235.0, q0=220,
                  drna=39000, dstrvm=80, lon=106.6333, lat=30.4559, addr="广安市广安区"),
             dict(stnm="广安西溪河站", river="西溪河", hnnm="嘉陵江", size="S", z0=245.0, q0=6,
                  drna=700, dstrvm=35, lon=106.4500, lat=30.5300, addr="广安市岳池县"),
         ]),
    dict(code="511700", name="达州市", short="达州", region="basin", rain=1200, phcd="DZ",
         stations=[
             dict(stnm="达州州河站", river="州河", hnnm="嘉陵江", size="M", z0=270.0, q0=140,
                  drna=11000, dstrvm=210, lon=107.4682, lat=31.2096, addr="达州市通川区"),
             dict(stnm="达州巴河站", river="巴河", hnnm="嘉陵江", size="M", z0=285.0, q0=55,
                  drna=4500, dstrvm=15, lon=107.5500, lat=31.3500, addr="达州市宣汉县"),
         ]),
    dict(code="511800", name="雅安市", short="雅安", region="basin", rain=1700, phcd="YA",
         stations=[
             dict(stnm="雅安青衣江站", river="青衣江", hnnm="岷江", size="M", z0=560.0, q0=210,
                  drna=9000, dstrvm=150, lon=103.0132, lat=29.9805, addr="雅安市雨城区"),
             dict(stnm="雅安大渡河站", river="大渡河", hnnm="岷江", size="L", z0=640.0, q0=1100,
                  drna=68000, dstrvm=250, lon=102.3600, lat=29.2300, addr="雅安市石棉县"),
         ]),
    dict(code="511900", name="巴中市", short="巴中", region="basin", rain=1150, phcd="BZ",
         stations=[
             dict(stnm="巴中巴河站", river="巴河", hnnm="嘉陵江", size="M", z0=350.0, q0=95,
                  drna=9500, dstrvm=160, lon=106.7473, lat=31.8672, addr="巴中市巴州区"),
             dict(stnm="巴中通江站", river="通江", hnnm="嘉陵江", size="S", z0=365.0, q0=25,
                  drna=2800, dstrvm=30, lon=107.2400, lat=31.9100, addr="巴中市通江县"),
         ]),
    dict(code="512000", name="资阳市", short="资阳", region="basin", rain=950, phcd="ZY",
         stations=[
             dict(stnm="资阳沱江站", river="沱江", hnnm="沱江", size="M", z0=340.0, q0=200,
                  drna=12000, dstrvm=230, lon=104.6278, lat=30.1286, addr="资阳市雁江区"),
             dict(stnm="资阳阳化河站", river="阳化河", hnnm="沱江", size="S", z0=350.0, q0=10,
                  drna=1200, dstrvm=30, lon=104.8500, lat=30.0500, addr="资阳市安岳县"),
         ]),
    dict(code="513200", name="阿坝藏族羌族自治州", short="阿坝", region="plateau", rain=700, phcd="AB",
         stations=[
             dict(stnm="阿坝岷江松潘站", river="岷江", hnnm="岷江", size="M", z0=2800.0, q0=60,
                  drna=4200, dstrvm=1300, lon=103.5930, lat=32.6380, addr="阿坝州松潘县"),
             dict(stnm="阿坝黑河若尔盖站", river="黑河", hnnm="黄河", size="S", z0=3400.0, q0=12,
                  drna=2600, dstrvm=300, lon=102.9600, lat=33.5800, addr="阿坝州若尔盖县",
                  bsnm="黄河流域"),
         ]),
    dict(code="513300", name="甘孜藏族自治州", short="甘孜", region="plateau", rain=650, phcd="GZ",
         stations=[
             dict(stnm="甘孜雅砻江雅江站", river="雅砻江", hnnm="雅砻江", size="L", z0=2600.0, q0=600,
                  drna=65000, dstrvm=700, lon=101.0100, lat=30.0300, addr="甘孜州雅江县"),
             dict(stnm="甘孜大渡河泸定站", river="大渡河", hnnm="岷江", size="L", z0=1330.0, q0=900,
                  drna=58000, dstrvm=600, lon=102.2300, lat=29.9100, addr="甘孜州泸定县"),
         ]),
    dict(code="513400", name="凉山彝族自治州", short="凉山", region="panzhihua", rain=1000, phcd="LSN",
         stations=[
             dict(stnm="凉山安宁河西昌站", river="安宁河", hnnm="雅砻江", size="M", z0=1500.0, q0=180,
                  drna=11000, dstrvm=200, lon=102.2644, lat=27.8816, addr="凉山州西昌市"),
             dict(stnm="凉山金沙江会东站", river="金沙江", hnnm="金沙江", size="L", z0=850.0, q0=1700,
                  drna=280000, dstrvm=560, lon=102.5800, lat=26.6300, addr="凉山州会东县"),
         ]),
]

# 暴雨洪水事件：(起始日期, 持续天数, 受影响市州码集合或 'BASIN', 强度放大倍数)
STORM_EVENTS = [
    (datetime(2025, 7, 12).date(), 2, "BASIN", 3.2),
    (datetime(2025, 8, 19).date(), 2, {"510800", "511300", "511600", "511700", "511900"}, 3.8),
    (datetime(2026, 6, 20).date(), 1, {"510400", "513400"}, 3.0),
    (datetime(2026, 7, 17).date(), 2, "BASIN", 3.5),
    (datetime(2026, 8, 9).date(), 2, {"510100", "510600", "510700", "511800", "511100", "511400"}, 4.2),
]

# 月雨日概率（1~12 月）
PWET = {
    "basin":     [0.18, 0.22, 0.32, 0.42, 0.52, 0.58, 0.60, 0.58, 0.55, 0.42, 0.28, 0.18],
    "plateau":   [0.10, 0.12, 0.20, 0.30, 0.45, 0.60, 0.65, 0.62, 0.55, 0.35, 0.15, 0.08],
    "panzhihua": [0.06, 0.08, 0.12, 0.20, 0.38, 0.55, 0.62, 0.60, 0.52, 0.32, 0.12, 0.06],
}
# 月平均基流倍数（含融雪/季节变化）
BASEFLOW = {
    "basin":     [0.55, 0.50, 0.55, 0.70, 0.95, 1.25, 1.50, 1.45, 1.20, 0.90, 0.70, 0.60],
    "plateau":   [0.45, 0.45, 0.60, 0.90, 1.20, 1.40, 1.50, 1.40, 1.20, 0.85, 0.60, 0.50],
    "panzhihua": [0.50, 0.45, 0.50, 0.65, 0.90, 1.25, 1.50, 1.45, 1.20, 0.90, 0.65, 0.55],
}
# 雨强月分配系数
IMONTH = [0.35, 0.40, 0.55, 0.75, 1.00, 1.30, 1.70, 1.60, 1.20, 0.80, 0.50, 0.35]

# 河流大小 → 汇流/量级参数
SIZE_PARAMS = {
    # rc 径流系数, kf/ks 快慢水库滞时(h), cf/cs 快慢流系数, v0 基准流速, b 水位流量关系指数
    "S": dict(rc=0.55, kf=4.0, ks=200.0, cf=0.90, cs=0.050, v0=0.9, b=2.0,
              f_warn=5.0, f_guar=9.0),
    "M": dict(rc=0.45, kf=10.0, ks=400.0, cf=0.62, cs=0.035, v0=1.5, b=2.0,
              f_warn=4.5, f_guar=8.0),
    "L": dict(rc=0.35, kf=24.0, ks=800.0, cf=0.26, cs=0.020, v0=2.2, b=2.0,
              f_warn=3.5, f_guar=6.0),
}

D0 = 2.0  # 常水位对应水深 (m)，Zref = z0 - D0

# ---------------------------------------------------------------------------
# 表属性 / 字段属性字典（ST_TABLE_D / ST_FIELD_D）
# ---------------------------------------------------------------------------
TABLE_META = {
    # tabid: (tabno, tabcn, taben, exchfl)
    "ST_TABLE_D":   ("ST_006_0001", "表属性信息表", "TableInfo", "0"),
    "ST_FIELD_D":   ("ST_006_0002", "字段属性信息表", "FieldInfo", "0"),
    "ST_ADDVCD_D":  ("ST_006_0004", "行政区域代码表", "AdminDivisionCode", "1"),
    "ST_STBPRP_B":  ("ST_001_0001", "测站基本属性表", "StationBasicInfo", "1"),
    "ST_RVFCCH_B":  ("ST_001_0005", "河道站防洪指标表", "RiverFloodControlCharacteristic", "1"),
    "ST_HIWRCH_B":  (None, "水情预警指标表", "HydrologicalWarningCharacteristic", "1"),
    "ST_PPTN_R":    ("ST_002_0001", "降水量表", "Precipitation", "1"),
    "ST_RIVER_R":   ("ST_002_0005", "河道水情表", "RiverRegime", "1"),
    "ST_FORECAST_F": ("ST_003_0002", "水情预报成果表", "HydrologicalForecast", "1"),
}

PK_ORDER = {
    "ST_TABLE_D": {"TABID": "1"},
    "ST_FIELD_D": {"TABID": "1", "FLID": "2"},
    "ST_ADDVCD_D": {"ADDVCD": "1"},
    "ST_STBPRP_B": {"STCD": "1"},
    "ST_RVFCCH_B": {"STCD": "1"},
    "ST_HIWRCH_B": {"STCD": "1", "HIWRTY": "2", "HIWRCH": "3"},
    "ST_PPTN_R": {"TM": "1", "STCD": "2"},
    "ST_RIVER_R": {"TM": "1", "STCD": "2"},
    "ST_FORECAST_F": {"STCD": "1", "UNITNAME": "2", "PLCD": "3",
                      "FYMDH": "4", "IYMDH": "5", "YMDH": "6"},
}

FIELD_RANGE = {
    "ST_PPTN_R.DRP": "指定时段降水量，精度0.1毫米；无雨为0",
    "ST_PPTN_R.INTV": "时段长HH.NN，NN为分钟；本数据集为1.00（1小时）",
    "ST_PPTN_R.PDR": "实际降雨历时HH.NN，分钟00~59；不大于INTV，无雨为0",
    "ST_PPTN_R.DYP": "前一日08:00至当日08:00累计；仅08:00且资料完整时填写",
    "ST_PPTN_R.WTH": "5雪、6雨夹雪、7雨、8阴、9晴",
    "ST_PPTN_R.GGMD": "0或空为自动观测，1为人工观测",
    "ST_RIVER_R.FLWCHRCD": "1干涸、2断流、3流向不定、4逆流、5起涨、6洪峰、P水电厂发电流量",
    "ST_RIVER_R.WPTN": "4落、5涨、6平",
    "ST_RIVER_R.MSQMT": "1水位流量关系曲线、2浮标及溶液、3流速仪及量水建筑物、4估算法、5ADCP、6电功率反推、9其它",
    "ST_RIVER_R.MSAMT": "1水位面积关系曲线、2测深杆或测深锤铅鱼、3回声测深仪、5ADCP、9其它",
    "ST_RIVER_R.MSVMT": "1流速仪、2浮标法、3声学法、5ADCP、9其它",
    "ST_STBPRP_B.STTP": "ZZ河道水文站、ZQ河道水位站、PP降水量站、RR水库站、DD闸坝站、TT潮位站",
    "ST_STBPRP_B.FRGRD": "1中央报汛、2省级报汛、3市级报汛、4县级报汛",
    "ST_STBPRP_B.STBK": "1左岸、2右岸",
    "ST_STBPRP_B.USFL": "1启用、0停用",
    "ST_STBPRP_B.ESSTYM": "建站年月，格式YYYYMM",
    "ST_STBPRP_B.BGFRYM": "始报年月，格式YYYYMM",
    "ST_STBPRP_B.ADDVCD": "GB/T 2260行政区划代码，6位数字",
    "ST_ADDVCD_D.ADDVCD": "GB/T 2260行政区划代码，6位数字，保留前导零",
    "ST_TABLE_D.EXCHFL": "0不交换、1交换",
    "ST_FIELD_D.NLAT": "N不允许空值，空字符串允许空值",
    "ST_FIELD_D.EUFL": "1枚举字段，空字符串非枚举",
    "ST_FIELD_D.PKAT": "主键序号，空字符串表示非主键",
    "ST_HIWRCH_B.HIWRTY": "0洪水、1干旱",
    "ST_HIWRCH_B.HIWRCH": "0水位、1流量、2水量",
    "ST_HIWRCH_B.WRSTAATR": "00瞬时、01一天、10十天",
    "ST_RIVER_R.Z": "水位，m，与流量满足本站水位流量关系曲线",
    "ST_RIVER_R.Q": "流量，m3/s，非负",
    "ST_FORECAST_F.Z": "预报水位，m",
    "ST_FORECAST_F.Q": "预报流量，m3/s",
}
ENUM_FIELDS = {k for k in FIELD_RANGE if k.split(".")[1] in {
    "WTH", "GGMD", "FLWCHRCD", "WPTN", "MSQMT", "MSAMT", "MSVMT",
    "STTP", "FRGRD", "STBK", "USFL", "EXCHFL", "NLAT", "EUFL",
    "HIWRTY", "HIWRCH", "WRSTAATR"}}

UNIT_OVERRIDE = {
    "ST_PPTN_R.INTV": "h（HH.NN）", "ST_PPTN_R.PDR": "h（HH.NN）",
    "ST_STBPRP_B.LGTD": "°", "ST_STBPRP_B.LTTD": "°",
    "ST_STBPRP_B.STAZT": "°", "ST_STBPRP_B.DSTRVM": "km",
    "ST_STBPRP_B.DRNA": "km²", "ST_STBPRP_B.ESSTYM": "YYYYMM",
    "ST_STBPRP_B.BGFRYM": "YYYYMM",
}
KNOWN_UNITS = {"m", "m3/s", "mm", "m2", "m/s", "km2", "度"}


def r3(x):
    return round(float(x), 3)


def fmt(dt):
    return dt.strftime("%Y-%m-%d %H:%M:%S")


# ---------------------------------------------------------------------------
# 单站序列生成
# ---------------------------------------------------------------------------
def gen_rainfall(rng, city, st, hours_month):
    """逐日 storm-cell 降雨模型，返回小时雨量数组（未取整）。"""
    P = np.zeros(HOURS)
    region = city["region"]
    base_intensity = city["rain"] / 1000.0
    for day in range(DAYS):
        date = START.date() + timedelta(days=day)
        m = date.month
        mult = 1.0
        forced = False
        for ev_date, ev_days, ev_scope, ev_mult in STORM_EVENTS:
            if ev_date <= date < ev_date + timedelta(days=ev_days):
                if ev_scope == "BASIN":
                    hit = region != "plateau"
                else:
                    hit = city["code"] in ev_scope
                if hit:
                    mult = ev_mult
                    forced = True
        pwet = 1.0 if forced else PWET[region][m - 1]
        if rng.random() >= pwet:
            continue
        ncell = 1 + int(rng.random() < 0.35) + (1 if forced else 0)
        for _ in range(ncell):
            dur = int(rng.integers(2, 11))
            if forced:
                dur = min(24, int(dur * 1.8))
            start = day * 24 + int(rng.integers(0, 24))
            peak = rng.exponential(base_intensity * IMONTH[m - 1]) * mult
            for k in range(dur):
                idx = start + k
                if idx >= HOURS:
                    break
                shape = math.sin(math.pi * (k + 0.5) / dur) ** 1.5
                P[idx] += peak * shape * rng.lognormal(0.0, 0.35)
    # 按年标定到目标年雨量（±15% 随机），保持过程形状
    for yi, (a, b) in enumerate([(0, 365 * 24), (365 * 24, HOURS)]):
        total = P[a:b].sum()
        target = city["rain"] * rng.uniform(0.85, 1.15)
        if total > 0:
            P[a:b] *= target / total
    return P


def gen_station(rng, city, st, stcd):
    """生成一站的全部小时序列与防洪指标。返回 dict。"""
    sp = SIZE_PARAMS[st["size"]]
    z0, q0 = st["z0"], st["q0"]
    zref = z0 - D0
    a = q0 / (D0 ** sp["b"])           # Q = a*(Z-Zref)^b
    bexp = sp["b"]

    def z_of_q(q):
        return zref + (q / a) ** (1.0 / bexp)

    # ---- 降雨 ----
    P_raw = gen_rainfall(rng, city, st, None)
    P = np.round(P_raw, 1)             # 0.1mm 精度
    P[P < 0.05] = 0.0

    # ---- 汇流（双线性水库）----
    dates = [START + timedelta(hours=h) for h in range(HOURS)]
    months = np.array([d.month for d in dates])
    base = np.array(BASEFLOW[city["region"]])[months - 1]
    eff = sp["rc"] * P
    af = math.exp(-1.0 / sp["kf"])
    as_ = math.exp(-1.0 / sp["ks"])
    F = S = 0.0
    mult = np.empty(HOURS)
    for t in range(HOURS):
        F = F * af + eff[t] * (1 - af)
        S = S * as_ + 0.25 * eff[t] * (1 - as_)
        mult[t] = base[t] + sp["cf"] * F + sp["cs"] * S
    # AR(1) 相对噪声
    noise = np.empty(HOURS)
    n = 0.0
    for t in range(HOURS):
        n = 0.92 * n + rng.normal(0.0, 0.025)
        noise[t] = n
    mult *= (1.0 + noise)

    # ---- 防洪阈值（先算堤顶，再限幅，保证最高水位不越堤顶）----
    wrq = q0 * sp["f_warn"]
    grq = q0 * sp["f_guar"]
    flpq = 0.75 * wrq
    sfq = 0.85 * wrq
    taq = 0.5 * (wrq + grq)
    laq = 0.45 * q0
    wrz, grz = z_of_q(wrq), z_of_q(grq)
    ldkel = grz + 2.0
    rdkel = ldkel + 0.2
    m_cap = ((ldkel - 0.25 - zref) / D0) ** bexp   # 水位上限低于左堤顶 0.25m
    mult = np.clip(mult, 0.25, m_cap)

    Q = np.round(q0 * mult, 3)
    Z = np.round(zref + (Q / a) ** (1.0 / bexp), 3)
    V = np.clip(sp["v0"] * (Q / q0) ** 0.28, 0.3, 6.0)
    XSAVV = np.round(V, 3)
    XSA = np.round(Q / XSAVV, 3)
    XSMXV = np.round(V * 1.22, 3)

    # ---- 水势 / 河水特征码 ----
    dz = np.diff(Z, prepend=Z[0])
    WPTN = np.where(dz > 0.005, "5", np.where(dz < -0.005, "4", "6"))
    WPTN[0] = "6"
    FLW = np.full(HOURS, None, dtype=object)
    sfq_v = sfq
    for t in range(6, HOURS - 6):
        if Q[t] > sfq_v and Q[t] >= Q[t - 1] and Q[t] > Q[t + 1] \
                and Q[t] == max(Q[t - 6:t + 7]):
            FLW[t] = "6"                      # 洪峰
    # 起涨：流量向上穿越前 24h 均值的 1.5 倍
    csum = np.concatenate([[0.0], np.cumsum(Q)])
    for t in range(24, HOURS):
        mean_prev = (csum[t] - csum[t - 24]) / 24.0
        mean_prev2 = (csum[t - 1] - csum[t - 25]) / 24.0
        if Q[t] > 1.5 * mean_prev and Q[t - 1] <= 1.5 * mean_prev2 \
                and Q[t] > 1.2 * q0:
            if FLW[t] is None:
                FLW[t] = "5"

    # ---- 天气状况 ----
    cold = st["z0"] >= 2500 or city["region"] == "plateau"
    WTH = np.empty(HOURS, dtype=object)
    day_rain = np.zeros(DAYS, dtype=bool)
    for t in range(HOURS):
        if P[t] > 0:
            day_rain[t // 24] = True
    for t in range(HOURS):
        m = dates[t].month
        if P[t] > 0:
            if cold and m in (11, 12, 1, 2, 3):
                WTH[t] = "5" if rng.random() < 0.75 else "6"
            elif m == 1 and rng.random() < 0.05:
                WTH[t] = "6"
            else:
                WTH[t] = "7"
        else:
            if day_rain[t // 24]:
                WTH[t] = "8" if rng.random() < 0.8 else "9"
            else:
                WTH[t] = "9" if rng.random() < 0.7 else "8"

    # ---- 降水历时 PDR（HH.NN，分钟）----
    PDR = np.zeros(HOURS)
    wet_idx = np.nonzero(P > 0)[0]
    mins = rng.integers(10, 61, size=len(wet_idx))
    PDR[wet_idx] = np.where(mins >= 60, 1.0, mins / 100.0)

    # ---- 日降水量 DYP：仅 08:00，前日 09:00~当日 08:00 共 24 时段 ----
    DYP = np.full(HOURS, None, dtype=object)
    for day in range(1, DAYS):           # 第 1 天 08:00 资料不完整 → NULL
        t8 = day * 24 + 8
        DYP[t8] = round(float(P[t8 - 23:t8 + 1].sum()), 1)

    # ---- 极值（供防洪指标表使用）----
    imax_q = int(np.argmax(Q)); imin_q = int(np.argmin(Q))
    imax_z = int(np.argmax(Z)); imin_z = int(np.argmin(Z))
    imax_v = int(np.argmax(XSAVV))
    hmxs = round(float(rng.uniform(3.0, 12.0)), 3)

    thresholds = dict(
        wrq=r3(wrq), grq=r3(grq), flpq=r3(flpq), sfq=r3(sfq), taq=r3(taq), laq=r3(laq),
        wrz=r3(wrz), grz=r3(grz), sfz=r3(z_of_q(sfq)), taz=r3(z_of_q(taq)),
        laz=r3(z_of_q(laq)), ldkel=r3(ldkel), rdkel=r3(rdkel),
    )

    return dict(
        stcd=stcd, dates=dates, P=P, PDR=PDR, DYP=DYP, WTH=WTH,
        Q=Q, Z=Z, XSA=XSA, XSAVV=XSAVV, XSMXV=XSMXV, WPTN=WPTN, FLW=FLW,
        thresholds=thresholds, a=a, b=bexp, zref=zref, q0=q0,
        extremes=dict(
            obhtz=r3(Z[imax_z]), obhtztm=dates[imax_z],
            obmxq=r3(Q[imax_q]), obmxqtm=dates[imax_q],
            hlz=r3(Z[imin_z]), hlztm=dates[imin_z],
            hmnq=r3(Q[imin_q]), hmnqtm=dates[imin_q],
            hmxavv=r3(XSAVV[imax_v]), hmxavvtm=dates[imax_v],
            hmxs=hmxs, hmxstm=dates[imax_q],
        ),
        annual_rain=[round(float(P[:365 * 24].sum()), 1),
                     round(float(P[365 * 24:].sum()), 1)],
        max_daily_rain=round(float(max(P[d * 24:(d + 1) * 24].sum() for d in range(DAYS))), 1),
    )


# ---------------------------------------------------------------------------
# 行构造
# ---------------------------------------------------------------------------
PPTN_COLS = ["STCD", "TM", "DRP", "INTV", "PDR", "DYP", "WTH", "GGMD"]
RIVER_COLS = ["STCD", "TM", "Z", "Q", "XSA", "XSAVV", "XSMXV",
              "FLWCHRCD", "WPTN", "MSQMT", "MSAMT", "MSVMT"]
FORECAST_COLS = ["STCD", "UNITNAME", "PLCD", "FYMDH", "IYMDH", "YMDH", "Z", "Q"]
UNITNAME = "四川省水文水资源勘测中心"
PLCD = "SC-DEMO-01"


def pptn_rows(g):
    for t in range(HOURS):
        yield (g["stcd"], fmt(g["dates"][t]), float(g["P"][t]), 1.00,
               float(g["PDR"][t]),
               None if g["DYP"][t] is None else float(g["DYP"][t]),
               g["WTH"][t], "0")


def river_rows(g):
    for t in range(HOURS):
        yield (g["stcd"], fmt(g["dates"][t]), float(g["Z"][t]), float(g["Q"][t]),
               float(g["XSA"][t]), float(g["XSAVV"][t]), float(g["XSMXV"][t]),
               g["FLW"][t], g["WPTN"][t], "1", "1", "9")


def forecast_rows(rng, g):
    for day in range(DAYS):
        fymdh = START + timedelta(days=day, hours=7)
        iymdh = START + timedelta(days=day, hours=8)
        for lead in (6, 12, 24):
            target = iymdh + timedelta(hours=lead)
            if target > END:
                continue
            t = day * 24 + 8 + lead
            z_err = rng.normal(0.0, 0.02 + 0.01 * lead / 6.0)
            q_err = rng.normal(0.0, 0.02 + 0.015 * lead / 6.0)
            zf = r3(g["Z"][t] + z_err)
            qf = r3(max(0.1, g["Q"][t] * (1 + q_err)))
            yield (g["stcd"], UNITNAME, PLCD, fmt(fymdh), fmt(iymdh),
                   fmt(target), zf, qf)


RVFCCH_COLS = ["STCD", "LDKEL", "RDKEL", "WRZ", "WRQ", "GRZ", "GRQ", "FLPQ",
               "OBHTZ", "OBHTZTM", "IVHZ", "IVHZTM", "OBMXQ", "OBMXQTM",
               "IVMXQ", "IVMXQTM", "HMXS", "HMXSTM", "HMXAVV", "HMXAVVTM",
               "HLZ", "HLZTM", "HMNQ", "HMNQTM", "TAZ", "TAQ", "LAZ", "LAQ",
               "SFZ", "SFQ", "MODITIME"]


def rvfcch_row(g):
    th, ex = g["thresholds"], g["extremes"]
    return (g["stcd"], th["ldkel"], th["rdkel"], th["wrz"], th["wrq"],
            th["grz"], th["grq"], th["flpq"],
            ex["obhtz"], fmt(ex["obhtztm"]),
            r3(ex["obhtz"] + 0.5), fmt(ex["obhtztm"]),      # 调查值略高于实测，同场洪水
            ex["obmxq"], fmt(ex["obmxqtm"]),
            r3(ex["obmxq"] * 1.08), fmt(ex["obmxqtm"]),
            ex["hmxs"], fmt(ex["hmxstm"]),
            ex["hmxavv"], fmt(ex["hmxavvtm"]),
            ex["hlz"], fmt(ex["hlztm"]), ex["hmnq"], fmt(ex["hmnqtm"]),
            th["taz"], th["taq"], th["laz"], th["laq"],
            th["sfz"], th["sfq"], fmt(SNAPSHOT))


HIWRCH_COLS = ["STCD", "HIWRTY", "HIWRCH", "WRSTAATR", "BUWR", "YEWR", "OGWR", "RDWR", "MODITIME"]


def hiwrch_rows(g):
    th = g["thresholds"]
    z_of_q = lambda q: g["zref"] + (q / g["a"]) ** (1.0 / g["b"])
    q0 = g["q0"]
    rows = [
        # 洪水：蓝=启动预报值，黄=警戒，橙=高水告警，红=保证
        ("0", "0", r3(th["sfz"]), r3(th["wrz"]), r3(th["taz"]), r3(th["grz"])),
        ("0", "1", r3(th["sfq"]), r3(th["wrq"]), r3(th["taq"]), r3(th["grq"])),
        # 干旱：阈值递减（水位/流量越低越严重）
        ("1", "0", r3(z_of_q(0.60 * q0)), r3(z_of_q(0.50 * q0)),
         r3(th["laz"]), r3(z_of_q(0.35 * q0))),
        ("1", "1", r3(0.60 * q0), r3(0.50 * q0), r3(th["laq"]), r3(0.35 * q0)),
    ]
    for ty, ch, bu, ye, og, rd in rows:
        yield (g["stcd"], ty, ch, "00", bu, ye, og, rd, fmt(SNAPSHOT))


STBPRP_COLS = ["STCD", "STNM", "RVNM", "HNNM", "BSNM", "LGTD", "LTTD", "STLC",
               "ADDVCD", "DTMNM", "DTMEL", "DTPR", "STTP", "FRGRD", "ESSTYM",
               "BGFRYM", "ATCUNIT", "ADMAUTH", "LOCALITY", "STBK", "STAZT",
               "DSTRVM", "DRNA", "PHCD", "USFL", "COMMENTS", "MODITIME", "RVCD"]


def stbprp_row(rng, city, st, stcd, idx, river_codes):
    bsnm = st.get("bsnm", "长江流域")
    rvcd = river_codes[st["river"]]
    return (stcd, st["stnm"], st["river"], st["hnnm"], bsnm,
            round(st["lon"], 6), round(st["lat"], 6),
            f"四川省{st['addr']}{st['river']}畔（模拟站址）",
            city["code"], "1985国家高程基准", 0.000, 0.000, "ZZ",
            "1" if idx == 0 else "2", "202501", "202501",
            "四川省水文水资源勘测中心", f"{city['short']}水文水资源勘测中心",
            "四川省", "1" if idx == 0 else "2",
            round(float(rng.uniform(0, 360)), 1),
            float(st["dstrvm"]), float(st["drna"]), city["phcd"], "1",
            "小时级合成测试数据（2025-01-01至2026-12-31）", fmt(SNAPSHOT), rvcd)


# ---------------------------------------------------------------------------
# 字典表
# ---------------------------------------------------------------------------
def table_d_rows():
    for tabid, (tabno, tabcn, taben, exchfl) in TABLE_META.items():
        yield (tabid, tabno, tabcn, taben, exchfl, fmt(SNAPSHOT))


def field_d_rows(schema_dump):
    tables = schema_dump["tables"]
    for tabid, tinfo in tables.items():
        for sid, (flid, f) in enumerate(tinfo["fields"].items(), start=1):
            comment = f.get("comment") or flid
            flcn = comment.split("，")[0].split(",")[0].strip()
            key = f"{tabid}.{flid}"
            # 类型描述
            tl = f["type"].split(" COLLATE")[0].lower().replace(" ", "")
            if tl == "datetime":
                tl = "datetime(3)"
            # 单位
            unit = UNIT_OVERRIDE.get(key)
            if unit is None:
                tail = comment.replace("，", ",").split(",")[-1].strip()
                unit = tail if tail in KNOWN_UNITS else None
            nlat = "N" if not f.get("nullable", True) else ""
            pkat = PK_ORDER.get(tabid, {}).get(flid, "")
            eufl = "1" if key in ENUM_FIELDS else ""
            fchsc = FIELD_RANGE.get(key)
            yield (tabid, flid, sid, flcn, flid, tl, nlat, unit, fchsc,
                   eufl, pkat, fmt(SNAPSHOT))


FIELD_D_COLS = ["TABID", "FLID", "FLSID", "FLCN", "FLEN", "FLTL", "NLAT",
                "UNNM", "FCHSC", "EUFL", "PKAT", "MODITIME"]
TABLE_D_COLS = ["TABID", "TABNO", "TABCN", "TABEN", "EXCHFL", "MODITIME"]
ADDVCD_COLS = ["ADDVCD", "ADDVNM", "COMMENTS", "MODITIME"]


def addvcd_rows():
    yield ("510000", "四川省", "模拟数据集省级区划", fmt(SNAPSHOT))
    for c in CITIES:
        yield (c["code"], c["name"], "四川省市州（模拟数据集区划）", fmt(SNAPSHOT))


# ---------------------------------------------------------------------------
# 导入与校验
# ---------------------------------------------------------------------------
ALL_TABLES = ["ST_TABLE_D", "ST_FIELD_D", "ST_ADDVCD_D", "ST_STBPRP_B",
              "ST_RVFCCH_B", "ST_HIWRCH_B", "ST_PPTN_R", "ST_RIVER_R", "ST_FORECAST_F"]


def insert_many(conn, table, cols, rows, batch=5000, label=""):
    cur = conn.cursor()
    sql = "INSERT INTO `{}` ({}) VALUES ({})".format(
        table, ",".join(f"`{c}`" for c in cols), ",".join(["%s"] * len(cols)))
    buf, n = [], 0
    for r in rows:
        buf.append(r)
        if len(buf) >= batch:
            cur.executemany(sql, buf)
            n += len(buf)
            buf = []
    if buf:
        cur.executemany(sql, buf)
        n += len(buf)
    conn.commit()
    if label:
        print(f"    {table:<14} {label} +{n} 行", flush=True)
    return n


def verify(conn, summary):
    cur = conn.cursor()
    print("\n========== 数据校验 ==========", flush=True)
    checks = []

    def check(name, sql, expect=None, info=False):
        cur.execute(sql)
        v = cur.fetchone()[0]
        ok = True if info else ((v == expect) if expect is not None else bool(v))
        checks.append((name, v, expect, ok))
        print(f"  [{'OK' if ok else 'FAIL'}] {name}: {v}" +
              (f"（期望 {expect}）" if expect is not None else ""), flush=True)
        return v

    counts = {}
    for t in ALL_TABLES:
        cur.execute(f"SELECT COUNT(*) FROM `{t}`")
        counts[t] = cur.fetchone()[0]
        print(f"  {t:<14} {counts[t]:>9} 行", flush=True)
    summary["row_counts"] = counts

    check("河道水情每站每天24条(异常组数)",
          "SELECT COUNT(*) FROM (SELECT STCD, DATE(TM) d, COUNT(*) c "
          "FROM ST_RIVER_R GROUP BY STCD, d HAVING c<>24) x", 0)
    check("降水量每站每天24条(异常组数)",
          "SELECT COUNT(*) FROM (SELECT STCD, DATE(TM) d, COUNT(*) c "
          "FROM ST_PPTN_R GROUP BY STCD, d HAVING c<>24) x", 0)
    check("河道水情时间范围",
          "SELECT CONCAT(DATE_FORMAT(MIN(TM),'%Y-%m-%d %H:%i:%s'),' ~ ',DATE_FORMAT(MAX(TM),'%Y-%m-%d %H:%i:%s')) FROM ST_RIVER_R",
          f"{fmt(START)} ~ {fmt(END)}")
    check("降水量时间范围",
          "SELECT CONCAT(DATE_FORMAT(MIN(TM),'%Y-%m-%d %H:%i:%s'),' ~ ',DATE_FORMAT(MAX(TM),'%Y-%m-%d %H:%i:%s')) FROM ST_PPTN_R",
          f"{fmt(START)} ~ {fmt(END)}")
    check("预报发生时间不越界(越界行数)",
          f"SELECT COUNT(*) FROM ST_FORECAST_F WHERE YMDH>'{fmt(END)}' OR YMDH<'{fmt(START)}'", 0)
    check("DYP仅在08时填写(违规行数)",
          "SELECT COUNT(*) FROM ST_PPTN_R WHERE DYP IS NOT NULL AND HOUR(TM)<>8", 0)
    check("DYP与24时段累计不符(行数,容差0.05mm)",
          "SELECT COUNT(*) FROM (SELECT STCD, TM, DYP, "
          "SUM(DRP) OVER (PARTITION BY STCD ORDER BY TM ROWS BETWEEN 23 PRECEDING AND CURRENT ROW) s "
          "FROM ST_PPTN_R) x WHERE DYP IS NOT NULL AND ABS(DYP-s)>0.05", 0)
    check("PDR>INTV(违规行数)",
          "SELECT COUNT(*) FROM ST_PPTN_R WHERE PDR>INTV", 0)
    check("WTH非法代码(行数)",
          "SELECT COUNT(*) FROM ST_PPTN_R WHERE WTH NOT IN ('5','6','7','8','9')", 0)
    check("有雨但WTH非降水码(行数)",
          "SELECT COUNT(*) FROM ST_PPTN_R WHERE DRP>0 AND WTH NOT IN ('5','6','7')", 0)
    check("XSA*XSAVV与Q偏差>2%(行数)",
          "SELECT COUNT(*) FROM ST_RIVER_R WHERE ABS(XSA*XSAVV-Q)>0.02*Q+0.5", 0)
    check("XSMXV<XSAVV(行数)",
          "SELECT COUNT(*) FROM ST_RIVER_R WHERE XSMXV<XSAVV", 0)
    check("水位流量关系阈值次序违规(站数)",
          "SELECT COUNT(*) FROM ST_RVFCCH_B WHERE NOT "
          "(LAZ<SFZ AND SFZ<WRZ AND WRZ<TAZ AND TAZ<GRZ AND GRZ<LDKEL AND LDKEL<RDKEL "
          "AND LAQ<SFQ AND SFQ<WRQ AND WRQ<TAQ AND TAQ<GRQ AND FLPQ<WRQ)", 0)
    check("实测最高水位越堤顶(站数)",
          "SELECT COUNT(*) FROM ST_RVFCCH_B WHERE OBHTZ>=LDKEL", 0)
    check("外键孤儿:降水量(行数)",
          "SELECT COUNT(*) FROM ST_PPTN_R p LEFT JOIN ST_STBPRP_B s ON p.STCD=s.STCD "
          "WHERE s.STCD IS NULL", 0)
    check("外键孤儿:河道水情(行数)",
          "SELECT COUNT(*) FROM ST_RIVER_R p LEFT JOIN ST_STBPRP_B s ON p.STCD=s.STCD "
          "WHERE s.STCD IS NULL", 0)
    check("外键孤儿:测站区划(行数)",
          "SELECT COUNT(*) FROM ST_STBPRP_B p LEFT JOIN ST_ADDVCD_D a ON p.ADDVCD=a.ADDVCD "
          "WHERE a.ADDVCD IS NULL", 0)
    check("区划码非四川(行数)",
          "SELECT COUNT(*) FROM ST_ADDVCD_D WHERE ADDVCD NOT LIKE '51%'", 0)
    check("超警戒水位小时数",
          "SELECT COUNT(*) FROM ST_RIVER_R r JOIN ST_RVFCCH_B f ON r.STCD=f.STCD WHERE r.Z>f.WRZ", info=True)
    check("超保证水位小时数",
          "SELECT COUNT(*) FROM ST_RIVER_R r JOIN ST_RVFCCH_B f ON r.STCD=f.STCD WHERE r.Z>f.GRZ", info=True)
    check("出现洪峰标记的站数",
          "SELECT COUNT(DISTINCT STCD) FROM ST_RIVER_R WHERE FLWCHRCD='6'", info=True)

    summary["checks"] = [dict(name=n, value=str(v), expect=str(e) if e is not None else None, ok=ok)
                         for n, v, e, ok in checks]
    failed = [c for c in checks if not c[3]]
    print(f"\n校验完成：{len(checks) - len(failed)}/{len(checks)} 项通过", flush=True)
    return not failed


# ---------------------------------------------------------------------------
def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--seed", type=int, default=20250101)
    ap.add_argument("--truncate", action="store_true", help="非空表先清空")
    ap.add_argument("--host", default=DB["host"])
    ap.add_argument("--port", type=int, default=DB["port"])
    ap.add_argument("--user", default=DB["user"])
    load_dotenv(Path(__file__).resolve().parents[1] / ".env")
    ap.add_argument("--password", default=os.getenv("DB_PASSWORD", ""))
    args = ap.parse_args()

    rng = np.random.default_rng(args.seed)
    conn = pymysql.connect(host=args.host, port=args.port, user=args.user,
                           password=args.password, database=DB["database"],
                           charset="utf8mb4", autocommit=False)
    cur = conn.cursor()

    # 表结构核对（以 schema.json 为准）
    schema = json.loads((ROOT / "NL2SQL" / "schema_cache" / "schema.json").read_text(encoding="utf-8"))
    dump = schema["mschema_dump"]
    for t in ALL_TABLES:
        cur.execute("SELECT COLUMN_NAME FROM information_schema.COLUMNS "
                    "WHERE TABLE_SCHEMA=%s AND TABLE_NAME=%s", (DB["database"], t))
        db_cols = {r[0] for r in cur.fetchall()}
        want = set(dump["tables"][t]["fields"].keys())
        if not want <= db_cols:
            print(f"[错误] 表 {t} 缺少字段: {want - db_cols}")
            sys.exit(1)

    # 非空检查
    nonempty = []
    for t in ALL_TABLES:
        cur.execute(f"SELECT COUNT(*) FROM `{t}`")
        if cur.fetchone()[0] > 0:
            nonempty.append(t)
    if nonempty:
        if not args.truncate:
            print(f"[退出] 以下表已有数据：{nonempty}；如需重建请加 --truncate")
            sys.exit(2)
        cur.execute("SET FOREIGN_KEY_CHECKS=0")
        for t in ALL_TABLES:
            cur.execute(f"TRUNCATE TABLE `{t}`")
        cur.execute("SET FOREIGN_KEY_CHECKS=1")
        conn.commit()
        print("已清空原有数据。")

    t0 = time.time()
    print(f"开始生成：seed={args.seed}，{len(CITIES)} 市州 × 2 站，"
          f"{fmt(START)} ~ {fmt(END)}（{HOURS} 小时/站）", flush=True)

    # ---- 静态表 ----
    n_table = insert_many(conn, "ST_TABLE_D", TABLE_D_COLS, table_d_rows(), label="字典")
    n_field = insert_many(conn, "ST_FIELD_D", FIELD_D_COLS, field_d_rows(dump), label="字典")
    n_add = insert_many(conn, "ST_ADDVCD_D", ADDVCD_COLS, addvcd_rows(), label="区划")

    # 河流代码（模拟）
    rivers = []
    for c in CITIES:
        for st in c["stations"]:
            if st["river"] not in rivers:
                rivers.append(st["river"])
    river_codes = {r: f"RV{i + 1:04d}" for i, r in enumerate(rivers)}

    # ---- 测站 ----
    stations = []
    stb_rows = []
    for c in CITIES:
        for idx, st in enumerate(c["stations"]):
            stcd = f"SC{c['code']}" if idx == 0 else f"SC{c['code'][:4]}01"
            stations.append((c, st, stcd))
            stb_rows.append(stbprp_row(rng, c, st, stcd, idx, river_codes))
    insert_many(conn, "ST_STBPRP_B", STBPRP_COLS, stb_rows, label="测站")

    # ---- 逐站小时数据 ----
    summary_stations = []
    totals = dict(pptn=0, river=0, forecast=0)
    for i, (c, st, stcd) in enumerate(stations, 1):
        g = gen_station(rng, c, st, stcd)
        totals["pptn"] += insert_many(conn, "ST_PPTN_R", PPTN_COLS, pptn_rows(g))
        totals["river"] += insert_many(conn, "ST_RIVER_R", RIVER_COLS, river_rows(g))
        totals["forecast"] += insert_many(conn, "ST_FORECAST_F", FORECAST_COLS, forecast_rows(rng, g))
        insert_many(conn, "ST_RVFCCH_B", RVFCCH_COLS, [rvfcch_row(g)])
        insert_many(conn, "ST_HIWRCH_B", HIWRCH_COLS, hiwrch_rows(g))
        ex = g["extremes"]
        summary_stations.append(dict(
            stcd=stcd, stnm=st["stnm"], addvcd=c["code"], city=c["name"],
            river=st["river"], size=st["size"], z0=st["z0"], q0=st["q0"],
            annual_rain_2025=g["annual_rain"][0], annual_rain_2026=g["annual_rain"][1],
            max_daily_rain=g["max_daily_rain"],
            max_z=ex["obhtz"], max_z_tm=fmt(ex["obhtztm"]),
            max_q=ex["obmxq"], max_q_tm=fmt(ex["obmxqtm"]),
            min_q=ex["hmnq"], wrz=g["thresholds"]["wrz"], grz=g["thresholds"]["grz"],
        ))
        print(f"  [{i:>2}/42] {stcd} {st['stnm']:<12} "
              f"年雨量 {g['annual_rain'][0]:.0f}/{g['annual_rain'][1]:.0f}mm  "
              f"最大日雨量 {g['max_daily_rain']:.0f}mm  "
              f"洪峰 {ex['obmxq']:.0f}m3/s @ {fmt(ex['obmxqtm'])}", flush=True)

    elapsed = time.time() - t0
    print(f"\n生成并导入完成，用时 {elapsed:.0f}s。"
          f"小时数据：降水量 {totals['pptn']} 行 / 河道水情 {totals['river']} 行 / "
          f"预报 {totals['forecast']} 行", flush=True)

    summary = dict(
        generated_at=fmt(datetime.now()), seed=args.seed,
        time_range=[fmt(START), fmt(END)], hours_per_station=HOURS,
        cities=len(CITIES), stations=len(stations),
        row_counts={}, stations_detail=summary_stations,
    )
    ok = verify(conn, summary)
    outdir = ROOT / "dataset"
    outdir.mkdir(exist_ok=True)
    (outdir / "sichuan_dataset_summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8")
    print(f"摘要已写入 {outdir / 'sichuan_dataset_summary.json'}")
    conn.close()
    sys.exit(0 if ok else 3)


if __name__ == "__main__":
    main()
